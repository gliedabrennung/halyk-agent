package classify

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/facts"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

const (
	ArtifactKind        = "labels"
	PatternArtifactKind = "label_patterns"
	PatternArtifactID   = "corpus"
)

const (
	SourceLLM       = "llm"
	SourceRuleLLM   = "rule+llm"
	SourceEscalated = "escalated"
	SourceRule      = "rule"
)

type Options struct {
	Cfg    *config.Config
	Store  *store.Store
	Log    *slog.Logger
	Client *llm.Client
	Only   []string

	Namespace string

	FactsNamespace string
}

type Report struct {
	Duration time.Duration `json:"duration"`

	Patterns    int `json:"patterns"`
	RuleMatched int `json:"rule_matched"`
	Agreed      int `json:"agreed"`
	Disputed    int `json:"disputed"`
	Escalated   int `json:"escalated"`
	Unknown     int `json:"unknown"`
	Calls       int `json:"calls"`

	Kept int `json:"kept"`

	Rows     []Row    `json:"rows"`
	Warnings []string `json:"warnings,omitempty"`
	Failed   []string `json:"failed,omitempty"`
	Path     string   `json:"path"`
}

type Row struct {
	ScenarioID string `json:"scenario_id"`
	Txns       int    `json:"txns"`
	Related    int    `json:"related"`
	Adjusted   int    `json:"adjusted"`
	Unknown    int    `json:"unknown"`
	Top        string `json:"top"`
}

func (r *Report) OK() bool { return r.Unknown == 0 }

func (r *Report) Degraded() bool { return len(r.Failed) > 0 }

func modelSettled(source string) bool {
	return source == SourceLLM || source == SourceRuleLLM || source == SourceEscalated
}

func keepBetter(labels, stored []domain.Label) int {
	if len(stored) == 0 {
		return 0
	}
	previous := make(map[string]domain.Label, len(stored))
	for _, l := range stored {
		previous[l.Pattern] = l
	}

	kept := 0
	for i := range labels {
		if modelSettled(labels[i].Source) {
			continue
		}
		prev, found := previous[labels[i].Pattern]
		if !found || !modelSettled(prev.Source) {
			continue
		}
		labels[i].Category, labels[i].Contra = prev.Category, prev.Contra
		labels[i].Source, labels[i].Confidence = prev.Source, prev.Confidence
		labels[i].Rationale, labels[i].Disputed = prev.Rationale, prev.Disputed
		kept++
	}
	return kept
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()

	tpl, txns, err := ingest.LoadTemplateAndTxns(opts.Cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	scenarios, err := tpl.ScenariosFor(opts.Only)
	if err != nil {
		return nil, err
	}
	var scoped []*domain.Txn
	for i := range txns {
		if slices.Contains(scenarios, txns[i].ScenarioID) {
			scoped = append(scoped, &txns[i])
		}
	}
	if len(scoped) == 0 {
		return nil, errors.New("the ledger holds no transactions for the template's scenarios")
	}

	patterns := groupPatterns(scoped)
	rep := &Report{Patterns: len(patterns)}

	labels, err := labelPatterns(ctx, opts, patterns, rep)
	if err != nil {
		return nil, err
	}

	var stored []domain.Label
	if _, err := opts.Store.GetArtifact(PatternArtifactKind+opts.Namespace, PatternArtifactID, &stored); err != nil {
		return nil, err
	}
	rep.Kept = keepBetter(labels, stored)
	if rep.Kept > 0 {
		opts.Log.Warn("this run degraded; keeping the labels the model settled before",
			"patterns", rep.Kept, "degraded_batches", len(rep.Failed))
	}

	dir := filepath.Join(opts.Cfg.ArtifactsDir, "labels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rep.Path = dir

	if err := opts.Store.PutArtifact(PatternArtifactKind+opts.Namespace, PatternArtifactID, labels); err != nil {
		return nil, err
	}
	if opts.Namespace == "" {
		if err := store.WriteJSON(filepath.Join(dir, "_patterns.json"), labels); err != nil {
			return nil, err
		}
	}

	byPattern := make(map[string]domain.Label, len(labels))
	for _, l := range labels {
		byPattern[l.Pattern] = l
	}

	for _, scn := range scenarios {
		set, warnings, err := buildLabelSet(opts, scn, scoped, byPattern)
		if err != nil {
			opts.Log.Error("borrower left without labels; the others continue", "scenario", scn, "err", err)
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %v", scn, err))
			continue
		}
		rep.Warnings = append(rep.Warnings, warnings...)
		if err := opts.Store.PutArtifact(ArtifactKind+opts.Namespace, scn, set); err != nil {
			return nil, err
		}
		if opts.Namespace == "" {
			if err := store.WriteJSON(filepath.Join(dir, scn+".json"), set); err != nil {
				return nil, err
			}
		}
		row := summarise(set)
		rep.Unknown += row.Unknown
		rep.Rows = append(rep.Rows, row)
		opts.Log.Info("labelled", "scenario", scn, "txns", row.Txns,
			"related", row.Related, "adjusted", row.Adjusted, "unknown", row.Unknown)
	}

	rep.Duration = time.Since(start)
	return rep, nil
}

type patternInfo struct {
	pattern           string
	outflows, inflows int

	unpriced       int
	samples        []string
	counterparties []string
	total          int
}

func groupPatterns(txns []*domain.Txn) []*patternInfo {
	const maxSamples = 3

	index := make(map[string]*patternInfo, len(txns))
	for _, t := range txns {
		key := Pattern(t.Description)
		p := index[key]
		if p == nil {
			p = &patternInfo{pattern: key}
			index[key] = p
		}
		p.total++
		switch {
		case t.AmountMissing:
			p.unpriced++
		case t.Amount.IsNegative():
			p.outflows++
		default:
			p.inflows++
		}
		if len(p.samples) < maxSamples {
			p.samples = append(p.samples, t.Description)
			p.counterparties = append(p.counterparties, t.Counterparty)
		}
	}
	out := make([]*patternInfo, 0, len(index))
	for _, p := range index {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *patternInfo) int { return strings.Compare(a.pattern, b.pattern) })
	return out
}

func labelPatterns(
	ctx context.Context,
	opts Options,
	patterns []*patternInfo,
	rep *Report,
) ([]domain.Label, error) {
	const (
		batchSize       = 50
		escalationBatch = 20
		minConfidence   = 0.6
	)

	items := make([]agents.ClassifyItem, len(patterns))
	ruleHit := make([]Rule, len(patterns))
	for i, p := range patterns {
		items[i] = agents.ClassifyItem{
			Pattern:        p.pattern,
			Count:          p.total,
			Outflows:       p.outflows,
			Inflows:        p.inflows,
			Unpriced:       p.unpriced,
			Samples:        p.samples,
			Counterparties: p.counterparties,
		}
		if r, ok := Classify(p.pattern); ok {
			ruleHit[i] = r
			rep.RuleMatched++
		}
	}

	results := make([]agents.ClassifyResult, len(patterns))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Cfg.MaxConcurrency)
	for lo := 0; lo < len(items); lo += batchSize {
		hi := min(lo+batchSize, len(items))
		g.Go(func() error {
			out, err := agents.ClassifyPatterns(gctx, opts.Client, opts.Cfg.Model, items[lo:hi])
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				opts.Log.Error("pattern batch failed; falling back to the keyword rules",
					"from", lo, "to", hi-1, "err", err)
				mu.Lock()
				for i := lo; i < hi; i++ {
					results[i] = agents.ClassifyResult{Category: domain.CatUnknown}
				}
				rep.Failed = append(rep.Failed, fmt.Sprintf("patterns %d-%d: %v", lo, hi-1, err))
				mu.Unlock()
				return nil
			}
			mu.Lock()
			copy(results[lo:hi], out)
			rep.Calls++
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var disputes []agents.Dispute
	var disputeIdx []int
	for i, p := range patterns {
		res := results[i]
		var reason string
		switch {
		case ruleHit[i].fired() && res.Category != ruleHit[i].Cat:
			reason = "different category"
		case ruleHit[i].fired() && res.Contra != ruleHit[i].Contra:
			reason = "different contra flag"
		case res.Category == domain.CatUnknown:
			reason = "the fast model would not classify it"
		case res.Confidence < minConfidence:
			reason = fmt.Sprintf("confidence %.2f", res.Confidence)
		case res.Contra && p.inflows == 0:
			reason = "marked as a reversal but every row is an outflow"
		}
		if reason == "" {
			continue
		}
		d := agents.Dispute{Item: items[i], ModelCat: res.Category, ModelCtr: res.Contra, Reason: reason}
		if ruleHit[i].fired() {
			d.RuleCat, d.RuleCtr = ruleHit[i].Cat, ruleHit[i].Contra
		} else {
			d.RuleCat = domain.CatUnknown
		}
		disputes = append(disputes, d)
		disputeIdx = append(disputeIdx, i)
	}
	rep.Disputed = len(disputes)

	resolved := make(map[int]agents.ClassifyResult, len(disputes))
	for lo := 0; lo < len(disputes); lo += escalationBatch {
		hi := min(lo+escalationBatch, len(disputes))
		out, err := agents.ResolveDisputes(ctx, opts.Client, opts.Cfg.Model, disputes[lo:hi])
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			opts.Log.Error("escalation batch failed; keeping the fast model's answer",
				"from", lo, "to", hi-1, "err", err)
			rep.Failed = append(rep.Failed, fmt.Sprintf("escalation %d-%d: %v", lo, hi-1, err))
			continue
		}
		rep.Calls++
		for k, r := range out {
			resolved[disputeIdx[lo+k]] = r
		}
	}
	rep.Escalated = len(resolved)
	rep.Agreed = len(patterns) - len(disputes)

	labels := make([]domain.Label, len(patterns))
	for i, p := range patterns {
		res := results[i]
		rule := ruleHit[i]
		correction, escalated := resolved[i]
		source := SourceLLM
		if escalated {
			res, source = correction, SourceEscalated
		} else if rule.fired() {

			source = SourceRuleLLM
		}
		if res.Category == domain.CatUnknown && rule.fired() {

			res.Category, res.Contra = rule.Cat, rule.Contra
			source = SourceRule
		}
		labels[i] = domain.Label{
			Pattern:      p.pattern,
			Category:     res.Category,
			Contra:       res.Contra,
			Source:       source,
			Confidence:   res.Confidence,
			Rationale:    res.Rationale,
			RuleCategory: rule.Cat,
			RuleID:       rule.ID,
			Disputed:     escalated,
			Count:        p.total,
			Samples:      p.samples,
		}
	}
	return labels, nil
}

func buildLabelSet(
	opts Options,
	scenarioID string,
	txns []*domain.Txn,
	byPattern map[string]domain.Label,
) (*domain.LabelSet, []string, error) {
	fb, err := store.RequireArtifact[domain.FactBase](
		opts.Store, facts.ArtifactKind+opts.FactsNamespace, scenarioID, "fact base", "facts")
	if err != nil {
		return nil, nil, err
	}
	return assemble(scenarioID, &fb, txns, byPattern)
}

func assemble(
	scenarioID string,
	fb *domain.FactBase,
	txns []*domain.Txn,
	byPattern map[string]domain.Label,
) (*domain.LabelSet, []string, error) {
	related, unresolved, aliases := relatedByCounterparty(fb, txns, scenarioID)

	set := &domain.LabelSet{
		ScenarioID: scenarioID,
		Company:    fb.Company,
		Totals:     make(map[domain.Category]decimal.Decimal),
	}
	matched := make(map[string]bool, len(byPattern))
	byID := make(map[string]int, len(txns))
	byEntity := make(map[string][]int, len(txns))

	for _, t := range txns {
		if t.ScenarioID != scenarioID {
			continue
		}
		lbl, found := byPattern[Pattern(t.Description)]
		if !found {
			return nil, nil, fmt.Errorf("%s: no label for pattern %q", t.ID, Pattern(t.Description))
		}
		tl := domain.TxnLabel{
			TxnID:      t.ID,
			Pattern:    lbl.Pattern,
			Category:   lbl.Category,
			Source:     lbl.Source,
			Confidence: lbl.Confidence,

			Contra:       lbl.Contra && !t.Amount.IsNegative() && !t.AmountMissing,
			Counterparty: t.Counterparty,
		}
		key := domain.EntityKey(t.Counterparty)
		if p, isRelated := related[key]; isRelated {
			tl.RelatedParty = true
			tl.PartyName = p.Name
			tl.VotingShare = p.VotingShare
			matched[key] = true
		}
		byID[t.ID] = len(set.Txns)
		byEntity[key] = append(byEntity[key], len(set.Txns))
		set.Totals[lbl.Category] = set.Totals[lbl.Category].Add(t.Amount)
		set.Txns = append(set.Txns, tl)
	}

	for key, p := range related {
		if matched[key] {
			set.RelatedParties = append(set.RelatedParties, p.Name)
		} else {
			set.UnmatchedParties = append(set.UnmatchedParties, p.Name)
		}
	}
	set.UnmatchedParties = append(set.UnmatchedParties, unresolved...)
	slices.Sort(set.RelatedParties)
	slices.Sort(set.UnmatchedParties)

	warnings := aliases
	warnings = append(warnings, markAdjustments(scenarioID, fb, set, byID, byEntity, txns)...)
	for _, name := range set.UnmatchedParties {
		warnings = append(warnings, fmt.Sprintf(
			"%s: related party %q has no ledger row under that name", scenarioID, name))
	}
	return set, warnings, nil
}

func relatedByCounterparty(
	fb *domain.FactBase,
	txns []*domain.Txn,
	scenarioID string,
) (map[string]domain.Party, []string, []string) {
	ledger := make(map[string]string)
	for _, t := range txns {
		if t.ScenarioID != scenarioID {
			continue
		}
		if key := domain.EntityKey(t.Counterparty); key != "" {
			ledger[key] = t.Counterparty
		}
	}

	related := make(map[string]domain.Party, len(fb.Parties))
	var unplaced []domain.Party
	for _, p := range fb.Parties {
		if !p.Related {
			continue
		}
		key := domain.EntityKey(p.Name)
		switch {
		case key == "":
		case ledger[key] != "":
			related[key] = p
		default:
			unplaced = append(unplaced, p)
		}
	}

	var unresolved, warnings []string
	for _, p := range unplaced {
		key := nearestCounterparty(domain.EntityKey(p.Name), ledger, related)
		if key == "" {
			unresolved = append(unresolved, p.Name)
			continue
		}
		related[key] = p
		warnings = append(warnings, fmt.Sprintf(
			"%s: no ledger row is booked to related party %q; %q is the only close name and is read as the same party",
			scenarioID, p.Name, ledger[key]))
	}
	return related, unresolved, warnings
}

func nearestCounterparty(want string, ledger map[string]string, taken map[string]domain.Party) string {
	if want == "" {
		return ""
	}
	hit := ""
	for key := range ledger {
		if _, used := taken[key]; used || keySimilarity(want, key) < _nameSimilarity {
			continue
		}
		if hit != "" {
			return ""
		}
		hit = key
	}
	return hit
}

func markAdjustments(
	scenarioID string,
	fb *domain.FactBase,
	set *domain.LabelSet,
	byID map[string]int,
	byEntity map[string][]int,
	txns []*domain.Txn,
) []string {
	amountOf := make(map[string]decimal.Decimal, len(txns))
	for _, t := range txns {
		if t.ScenarioID == scenarioID {
			amountOf[t.ID] = t.Amount
		}
	}

	var warnings []string
	for _, adj := range fb.Adjustments {
		if adj.TxnID == "" && adj.Counterparty == "" && !adj.Amount.IsPositive() {
			continue
		}
		idx := -1
		if adj.TxnID != "" {
			i, ok := byID[adj.TxnID]
			switch {
			case !ok:
				warnings = append(warnings, fmt.Sprintf(
					"%s: adjustment names %s, which is not a row of this borrower; matching by counterparty and amount instead",
					scenarioID, adj.TxnID))
			case contradictsRow(adj, set.Txns[i], amountOf):
				warnings = append(warnings, fmt.Sprintf(
					"%s: adjustment names %s, but that row is %q for %s while the disclosure says %q for %s; "+
						"ignoring the id and matching by counterparty and amount instead",
					scenarioID, adj.TxnID, set.Txns[i].Counterparty, amountOf[set.Txns[i].TxnID].Abs(),
					adj.Counterparty, adj.Amount.Abs()))
			default:
				idx = i
			}
		}
		if idx < 0 && adj.Counterparty != "" {
			idx = rowForCounterparty(adj, set, byEntity[domain.EntityKey(adj.Counterparty)], amountOf)
		}
		if idx < 0 && adj.Amount.IsPositive() {
			if i := rowForAmount(adj, set, amountOf); i >= 0 {
				idx = i
				warnings = append(warnings, fmt.Sprintf(
					"%s: no row is booked to %q, but %s carries its amount %s under the similar name %q; matched on both",
					scenarioID, adj.Counterparty, set.Txns[i].TxnID, adj.Amount, set.Txns[i].Counterparty))
			}
		}
		if idx < 0 {
			warnings = append(warnings, fmt.Sprintf("%s: adjustment for %q (%s) matches no row; left unmarked",
				scenarioID, adj.Counterparty, adj.Amount))
			continue
		}

		tl := &set.Txns[idx]
		tl.AdjustmentKind = adj.Kind
		if adj.Kind == domain.AdjReclassify && adj.Applied {
			tl.Reclassified = true
			if cat, ok := domain.CategoryForLine(adj.ToCategory); ok {
				tl.ReclassifiedTo = cat
			} else if adj.ToCategory != "" {
				warnings = append(warnings, fmt.Sprintf("%s: %s reclassified to %q, which maps to no category",
					scenarioID, tl.TxnID, adj.ToCategory))
			}
		}
	}
	return warnings
}

func contradictsRow(adj domain.Adjustment, tl domain.TxnLabel, amountOf map[string]decimal.Decimal) bool {
	if adj.Counterparty != "" && !similarName(adj.Counterparty, tl.Counterparty) {
		return true
	}
	if adj.Kind == domain.AdjLedgerAmountFix || !adj.Amount.IsPositive() {
		return false
	}
	return !amountOf[tl.TxnID].Abs().Equal(adj.Amount.Abs())
}

func rowForAmount(adj domain.Adjustment, set *domain.LabelSet, amountOf map[string]decimal.Decimal) int {
	hit := -1
	for i, tl := range set.Txns {
		if !amountOf[tl.TxnID].Abs().Equal(adj.Amount.Abs()) {
			continue
		}
		if adj.Counterparty != "" && !similarName(adj.Counterparty, tl.Counterparty) {
			continue
		}
		if hit >= 0 {
			return -1
		}
		hit = i
	}
	return hit
}

const _nameSimilarity = 0.6

func similarName(a, b string) bool {
	ka, kb := domain.EntityKey(a), domain.EntityKey(b)
	if ka == "" || kb == "" {
		return false
	}
	if ka == kb {
		return true
	}
	other := strings.Fields(kb)
	for _, word := range strings.Fields(ka) {
		if len([]rune(word)) >= 4 && slices.Contains(other, word) {
			return true
		}
	}
	return keySimilarity(ka, kb) >= _nameSimilarity
}

func keySimilarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	longest := max(len(ra), len(rb))
	if longest == 0 {
		return 0
	}
	return float64(longest-editDistance(ra, rb)) / float64(longest)
}

func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func rowForCounterparty(
	adj domain.Adjustment,
	set *domain.LabelSet,
	candidates []int,
	amountOf map[string]decimal.Decimal,
) int {
	if len(candidates) == 1 {
		return candidates[0]
	}
	if len(candidates) <= 1 || !adj.Amount.IsPositive() {
		return -1
	}
	hit := -1
	for _, c := range candidates {
		if !amountOf[set.Txns[c].TxnID].Abs().Equal(adj.Amount.Abs()) {
			continue
		}
		if hit >= 0 {
			return -1
		}
		hit = c
	}
	return hit
}

func summarise(set *domain.LabelSet) Row {
	row := Row{ScenarioID: set.ScenarioID, Txns: len(set.Txns)}
	counts := make(map[domain.Category]int, len(set.Txns))
	for _, t := range set.Txns {
		counts[t.Category]++
		if t.RelatedParty {
			row.Related++
		}
		if t.AdjustmentKind != "" {
			row.Adjusted++
		}
		if t.Category == domain.CatUnknown {
			row.Unknown++
		}
	}
	type kv struct {
		cat domain.Category
		n   int
	}
	pairs := make([]kv, 0, len(counts))
	for c, n := range counts {
		pairs = append(pairs, kv{c, n})
	}
	slices.SortFunc(pairs, func(a, b kv) int {
		if a.n != b.n {
			return cmp.Compare(b.n, a.n)
		}
		return cmp.Compare(a.cat, b.cat)
	})
	var parts []string
	for _, p := range pairs[:min(len(pairs), 4)] {
		parts = append(parts, fmt.Sprintf("%s=%d", p.cat, p.n))
	}
	row.Top = strings.Join(parts, " ")
	return row
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 104)
	fmt.Fprintf(&b, "\n%s\nCLASSIFICATION  (%.1fs)  %d patterns, %d model calls\n%s\n",
		line, r.Duration.Seconds(), r.Patterns, r.Calls, line)
	fmt.Fprintf(&b, "  rules matched %d/%d patterns; %d settled by rule+model agreement, %d escalated\n",
		r.RuleMatched, r.Patterns, r.Agreed, r.Escalated)
	fmt.Fprintf(&b, "\n  %-5s %6s %8s %9s %8s  %s\n", "scn", "txns", "related", "adjusted", "unknown", "top categories")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "  %-5s %6d %8d %9d %8d  %s\n",
			row.ScenarioID, row.Txns, row.Related, row.Adjusted, row.Unknown, row.Top)
	}
	fmt.Fprintf(&b, "%s\n", line)
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "  DEGRADED %d batches, keyword rules used instead:\n", len(r.Failed))
		for _, f := range r.Failed {
			fmt.Fprintf(&b, "    %s\n", f)
		}
		if r.Kept > 0 {
			fmt.Fprintf(&b, "  KEPT %d pattern(s) on the labels the model settled in an earlier run;"+
				" this run did not overwrite them\n", r.Kept)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "  WARNINGS:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "    %s\n", w)
		}
	}
	fmt.Fprintf(&b, "wrote %s/<scenario>.json and %s/_patterns.json\n%s\n", r.Path, r.Path, line)
	return b.String()
}
