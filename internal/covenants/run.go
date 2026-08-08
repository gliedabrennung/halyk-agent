package covenants

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
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	ArtifactKind = "covenants"
)

type Options struct {
	Cfg    *config.Config
	Store  *store.Store
	Log    *slog.Logger
	Client *llm.Client
	Only   []string

	Namespace    string
	CriticPasses int
}

type Report struct {
	Duration     time.Duration  `json:"duration"`
	Specs        int            `json:"specs"`
	Scenarios    int            `json:"scenarios"`
	WithTrigger  []string       `json:"with_trigger,omitempty"`
	WithCarveout []string       `json:"with_carveout,omitempty"`
	LowConfident []string       `json:"low_confidence,omitempty"`
	CriticFixed  []string       `json:"critic_fixed,omitempty"`
	ByUnit       map[string]int `json:"by_unit"`
	ByEvidence   map[string]int `json:"by_evidence"`
	TermKinds    map[string]int `json:"term_kinds"`
	Path         string         `json:"path"`
	Rows         []Row          `json:"rows"`
	Expected     int            `json:"expected"`
	Failed       []string       `json:"failed,omitempty"`
}

func (r *Report) OK() bool { return len(r.Failed) == 0 && r.Specs == r.Expected }

type Row struct {
	ScenarioID string  `json:"scenario_id"`
	ClauseID   string  `json:"clause_id"`
	Expression string  `json:"expression"`
	Op         string  `json:"op"`
	Threshold  string  `json:"threshold"`
	Unit       string  `json:"unit"`
	Evidence   string  `json:"evidence_kind"`
	Trigger    bool    `json:"trigger"`
	Carveouts  int     `json:"carveouts"`
	Critic     int     `json:"critic_passes"`
	Confidence float64 `json:"confidence"`
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	const defaultCriticPasses = 2

	start := time.Now()
	if opts.CriticPasses <= 0 {
		opts.CriticPasses = defaultCriticPasses
	}

	tpl, err := ingest.ParseTemplate(opts.Cfg.TemplatePath())
	if err != nil {
		return nil, err
	}
	idx, err := index.Load(opts.Store)
	if err != nil {
		return nil, err
	}

	scenarios, err := tpl.ScenariosFor(opts.Only)
	if err != nil {
		return nil, err
	}

	type job struct {
		scenario string
		clause   string
		input    agents.CovenantInput
	}
	var jobs []job

	for _, scn := range scenarios {
		clauses := tpl.ClausesFor(scn)
		agreements := idx.CreditAgreements(scn)
		if len(agreements) == 0 {
			return nil, fmt.Errorf("%s: no effective credit agreement in the index", scn)
		}

		agreement := agreements[0]
		for _, a := range agreements {
			if a.Scan.HasClauses(clauses) {
				agreement = a
				break
			}
		}

		text, err := opts.Store.DocText(agreement.DocID)
		if err != nil {
			return nil, fmt.Errorf("%s: load agreement %s: %w", scn, agreement.DocID, err)
		}
		article, err := CovenantArticleFor(text, clauses)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", scn, agreement.DocID, err)
		}
		definitions := definitionsText(text)
		amendments, err := amendmentText(opts.Store, idx, scn)
		if err != nil {
			return nil, err
		}

		for _, clauseID := range clauses {
			clauseText, err := Clause(article.Text, clauseID)
			if err != nil {
				return nil, fmt.Errorf("%s (%s): %w", scn, agreement.DocID, err)
			}
			jobs = append(jobs, job{
				scenario: scn,
				clause:   clauseID,
				input: agents.CovenantInput{
					ScenarioID:   scn,
					ClauseID:     clauseID,
					Company:      agreement.Meta.CompanyName,
					ClauseText:   clauseText,
					ArticleText:  article.Text,
					Definitions:  definitions,
					AmendmentsIn: amendments,
				},
			})
		}
		opts.Log.Info("covenant article located",
			"scenario", scn, "doc", agreement.DocID, "article", article.Number,
			"clauses", strings.Join(clauses, ","), "chars", len(article.Text))
	}

	specs := make([]*domain.CovenantSpec, len(jobs))
	failures := make([]string, len(jobs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Cfg.MaxConcurrency)
	var mu sync.Mutex
	done := 0

	for i, j := range jobs {
		g.Go(func() error {
			spec, err := agents.ExtractCovenant(gctx, opts.Client, opts.Cfg.Model, j.input, opts.CriticPasses)
			if err != nil {

				if gctx.Err() != nil {
					return gctx.Err()
				}
				opts.Log.Error("covenant extraction failed",
					"scenario", j.scenario, "clause", j.clause, "err", err)
				mu.Lock()
				failures[i] = fmt.Sprintf("%s/%s: %v", j.scenario, j.clause, err)
				done++
				mu.Unlock()
				return nil
			}
			spec.SourceRef.DocID = j.input.ScenarioID
			for _, note := range Normalise(spec) {
				opts.Log.Warn("specification normalised",
					"scenario", j.scenario, "clause", j.clause, "note", note)
			}
			specs[i] = spec

			mu.Lock()
			done++
			n := done
			mu.Unlock()
			opts.Log.Info("covenant extracted", "scenario", j.scenario, "clause", j.clause,
				"expr", spec.Expression, "op", spec.Op, "threshold", spec.Threshold.String(),
				"critic_passes", spec.CriticPasses, "done", n, "total", len(jobs))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var failed []string
	for _, f := range failures {
		if f != "" {
			failed = append(failed, f)
		}
	}

	byScenario := map[string][]*domain.CovenantSpec{}
	var extracted []*domain.CovenantSpec
	for _, s := range specs {
		if s == nil {
			continue
		}
		extracted = append(extracted, s)
		byScenario[s.ScenarioID] = append(byScenario[s.ScenarioID], s)
	}
	specs = extracted

	dir := filepath.Join(opts.Cfg.ArtifactsDir, "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for scn, list := range byScenario {
		slices.SortFunc(list, func(a, b *domain.CovenantSpec) int { return strings.Compare(a.ClauseID, b.ClauseID) })
		if err := opts.Store.PutArtifact(ArtifactKind+opts.Namespace, scn, list); err != nil {
			return nil, err
		}
		// Пробный прогон в своём namespace на диск не пишет: файлы принадлежат основному.
		if opts.Namespace != "" {
			continue
		}
		if err := store.WriteJSON(filepath.Join(dir, scn+".json"), list); err != nil {
			return nil, err
		}
	}

	rep := buildReport(specs, tpl.Scenarios)
	rep.Duration = time.Since(start)
	rep.Path = dir
	rep.Failed = failed
	rep.Expected = len(jobs)
	return rep, nil
}

func definitionsText(text string) string {
	const maxDefinitionsChars = 9000

	sec, err := Article(text, 1)
	if err != nil {
		return ""
	}
	s := sec.Text
	if len(s) > maxDefinitionsChars {
		s = s[:maxDefinitionsChars] + "\n[... truncated ...]"
	}
	return s
}

func amendmentText(st *store.Store, idx *index.Index, scenarioID string) (string, error) {
	docs := idx.DocsFor(scenarioID, domain.DocAmendment)
	if len(docs) == 0 {
		return "", nil
	}
	var parts []string
	for _, d := range docs {
		text, err := st.DocText(d.DocID)
		if err != nil {
			return "", fmt.Errorf("load amendment %s: %w", d.DocID, err)
		}
		parts = append(parts, fmt.Sprintf("[amendment %s, effective %s]\n%s",
			d.DocID, d.Meta.EffectiveDate, text))
	}
	return strings.Join(parts, "\n\n"), nil
}

func buildReport(specs []*domain.CovenantSpec, order []string) *Report {
	rep := &Report{
		Specs:      len(specs),
		ByUnit:     make(map[string]int),
		ByEvidence: make(map[string]int),
		TermKinds:  make(map[string]int),
	}
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		seen[s.ScenarioID] = true
		rep.ByUnit[s.Unit]++
		rep.ByEvidence[s.EvidenceKind]++
		for _, t := range s.Terms {
			rep.TermKinds[string(t.Kind)]++
		}
		cell := s.ScenarioID + "/" + s.ClauseID
		if s.Trigger != nil {
			rep.WithTrigger = append(rep.WithTrigger, cell)
		}
		if len(s.Carveouts) > 0 {
			rep.WithCarveout = append(rep.WithCarveout, cell)
		}
		if s.Confidence < 0.7 {
			rep.LowConfident = append(rep.LowConfident, fmt.Sprintf("%s(%.2f)", cell, s.Confidence))
		}
		for _, n := range s.CriticNotes {
			if !strings.Contains(n, "accepted") {
				rep.CriticFixed = append(rep.CriticFixed, cell)
				break
			}
		}
		rep.Rows = append(rep.Rows, Row{
			ScenarioID: s.ScenarioID, ClauseID: s.ClauseID, Expression: s.Expression,
			Op: s.Op, Threshold: s.Threshold.String(), Unit: s.Unit,
			Evidence: s.EvidenceKind, Trigger: s.Trigger != nil, Carveouts: len(s.Carveouts),
			Critic: s.CriticPasses, Confidence: s.Confidence,
		})
	}
	rep.Scenarios = len(seen)

	pos := make(map[string]int, len(order))
	for i, s := range order {
		pos[s] = i
	}
	slices.SortStableFunc(rep.Rows, func(a, b Row) int {
		return cmp.Or(
			cmp.Compare(pos[a.ScenarioID], pos[b.ScenarioID]),
			cmp.Compare(a.ClauseID, b.ClauseID))
	})
	slices.Sort(rep.WithTrigger)
	slices.Sort(rep.WithCarveout)
	slices.Sort(rep.CriticFixed)
	return rep
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 110)
	fmt.Fprintf(&b, "\n%s\nCOVENANT SPECS  (%.1fs)  %d specs for %d borrowers\n%s\n",
		line, r.Duration.Seconds(), r.Specs, r.Scenarios, line)
	fmt.Fprintf(&b, "  %-5s %-5s %-46s %-3s %14s %-6s %-9s %s\n",
		"scn", "claus", "expression", "op", "threshold", "unit", "evidence", "flags")
	for _, row := range r.Rows {
		flags := ""
		if row.Trigger {
			flags += "TRIGGER "
		}
		if row.Carveouts > 0 {
			flags += fmt.Sprintf("carveouts=%d ", row.Carveouts)
		}
		if row.Confidence < 0.7 {
			flags += fmt.Sprintf("conf=%.2f", row.Confidence)
		}
		fmt.Fprintf(&b, "  %-5s %-5s %-46s %-3s %14s %-6s %-9s %s\n",
			row.ScenarioID, row.ClauseID, truncate(row.Expression, 46), row.Op,
			row.Threshold, row.Unit, row.Evidence, flags)
	}
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "  units      %s\n", domain.JoinPairs(r.ByUnit))
	fmt.Fprintf(&b, "  evidence   %s\n", domain.JoinPairs(r.ByEvidence))
	fmt.Fprintf(&b, "  term kinds %s\n", domain.JoinPairs(r.TermKinds))
	if len(r.WithTrigger) > 0 {
		fmt.Fprintf(&b, "  triggers   %s\n", strings.Join(r.WithTrigger, " "))
	}
	if len(r.WithCarveout) > 0 {
		fmt.Fprintf(&b, "  carveouts  %s\n", strings.Join(r.WithCarveout, " "))
	}
	if len(r.CriticFixed) > 0 {
		fmt.Fprintf(&b, "  critic corrected %d: %s\n", len(r.CriticFixed), strings.Join(r.CriticFixed, " "))
	}
	if len(r.LowConfident) > 0 {
		fmt.Fprintf(&b, "  LOW CONFIDENCE %s\n", strings.Join(r.LowConfident, " "))
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "\n  FAILED %d of %d cells:\n", len(r.Failed), r.Expected)
		for _, f := range r.Failed {
			fmt.Fprintf(&b, "    %s\n", f)
		}
	}
	fmt.Fprintf(&b, "\nwrote %s/<scenario>.json\n%s\n", r.Path, line)
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
