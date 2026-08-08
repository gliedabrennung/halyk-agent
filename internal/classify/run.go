package classify

import (
	"cmp"
	"context"
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

const ArtifactKind = "labels"

const (
	PatternArtifactKind = "label_patterns"
	PatternArtifactID   = "corpus"
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

	Rows     []Row    `json:"rows"`
	Warnings []string `json:"warnings,omitempty"`
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
		return nil, fmt.Errorf("the ledger holds no transactions for the template's scenarios")
	}

	patterns := groupPatterns(scoped)
	rep := &Report{Patterns: len(patterns)}

	labels, err := labelPatterns(ctx, opts, patterns, rep)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(opts.Cfg.ArtifactsDir, "labels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rep.Path = dir

	err = opts.Store.PutArtifact(PatternArtifactKind+opts.Namespace, PatternArtifactID, labels)
	if err != nil {
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
			return nil, err
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
				return fmt.Errorf("patterns %d-%d: %w", lo, hi-1, err)
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
			return nil, err
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
		source := "llm"
		if escalated {
			res, source = correction, "escalated"
		} else if rule.fired() {

			source = "rule+llm"
		}
		if res.Category == domain.CatUnknown && rule.fired() {

			res.Category, res.Contra = rule.Cat, rule.Contra
			source = "rule"
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
	related := make(map[string]domain.Party, len(fb.Parties))
	for _, p := range fb.Parties {
		if !p.Related {
			continue
		}
		if key := domain.EntityKey(p.Name); key != "" {
			related[key] = p
		}
	}

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
	slices.Sort(set.RelatedParties)
	slices.Sort(set.UnmatchedParties)

	warnings := markAdjustments(scenarioID, fb, set, byID, byEntity, txns)
	for _, name := range set.UnmatchedParties {
		warnings = append(warnings, fmt.Sprintf(
			"%s: related party %q has no ledger row under that name", scenarioID, name))
	}
	return set, warnings, nil
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
		idx := -1
		switch {
		case adj.TxnID != "":
			i, ok := byID[adj.TxnID]
			if !ok {
				warnings = append(warnings, fmt.Sprintf(
					"%s: adjustment names %s, which is not a row of this borrower", scenarioID, adj.TxnID))
				continue
			}
			idx = i
		case adj.Counterparty != "":
			candidates := byEntity[domain.EntityKey(adj.Counterparty)]
			switch {
			case len(candidates) == 1:
				idx = candidates[0]
			case len(candidates) > 1 && adj.Amount.IsPositive():

				var hits []int
				for _, c := range candidates {
					if amountOf[set.Txns[c].TxnID].Abs().Equal(adj.Amount.Abs()) {
						hits = append(hits, c)
					}
				}
				if len(hits) == 1 {
					idx = hits[0]
				}
			}
			if idx < 0 {
				warnings = append(warnings, fmt.Sprintf("%s: adjustment for %q (%s) matches %d rows; left unmarked",
					scenarioID, adj.Counterparty, adj.Amount, len(candidates)))
				continue
			}
		default:

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
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "  WARNINGS:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "    %s\n", w)
		}
	}
	fmt.Fprintf(&b, "wrote %s/<scenario>.json and %s/_patterns.json\n%s\n", r.Path, r.Path, line)
	return b.String()
}
