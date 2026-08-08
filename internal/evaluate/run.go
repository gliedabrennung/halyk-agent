package evaluate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/classify"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/covenants"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/engine"
	"github.com/gliedabrennung/halyk-agent/internal/facts"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
)

type Options struct {
	Cfg   *config.Config
	Store *store.Store
	Log   *slog.Logger
	Only  []string

	Client *llm.Client

	Namespace string

	DryRun bool
}

type Report struct {
	Duration time.Duration `json:"duration"`
	Cells    int           `json:"cells"`
	Breaches int           `json:"breaches"`
	Evidence int           `json:"evidence"`
	Flagged  int           `json:"flagged"`
	Reviewed int           `json:"reviewed"`
	Disputed int           `json:"disputed"`

	Rows       []Row    `json:"rows"`
	Missing    []string `json:"missing,omitempty"`
	ReviewPath string   `json:"review_path"`
}

type Row struct {
	ScenarioID string `json:"scenario_id"`
	ClauseID   string `json:"clause_id"`
	Status     string `json:"status"`
	Actual     string `json:"actual"`
	Evidence   string `json:"evidence"`
	Note       string `json:"note,omitempty"`
}

func (r *Report) OK() bool { return len(r.Missing) == 0 }

func Run(ctx context.Context, opts Options) (*Report, error) {
	const reviewConfidence = 0.5
	start := time.Now()

	tpl, txns, err := ingest.LoadTemplateAndTxns(opts.Cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	fallbackFX, err := config.LoadFXRates(opts.Cfg.FXPath())
	if err != nil {
		return nil, err
	}

	scenarios, err := tpl.ScenariosFor(opts.Only)
	if err != nil {
		return nil, err
	}

	byScenario := make(map[string][]domain.Txn)
	for _, t := range txns {
		byScenario[t.ScenarioID] = append(byScenario[t.ScenarioID], t)
	}

	rep := &Report{}
	var reviews []reviewEntry
	var cells []cell
	candidatesOf := make(map[string][]string)

	for _, scn := range scenarios {
		specs, err := store.RequireArtifact[[]*domain.CovenantSpec](
			opts.Store, covenants.ArtifactKind+opts.Namespace, scn, "covenant specifications", "covenants")
		if err != nil {
			return nil, err
		}
		fb, err := store.RequireArtifact[domain.FactBase](
			opts.Store, facts.ArtifactKind+opts.Namespace, scn, "fact base", "facts")
		if err != nil {
			return nil, err
		}
		labels, err := store.RequireArtifact[domain.LabelSet](
			opts.Store, classify.ArtifactKind+opts.Namespace, scn, "labels", "classify")
		if err != nil {
			return nil, err
		}

		in := &engine.Inputs{
			ScenarioID: scn,
			Facts:      &fb,
			Labels:     &labels,
			Txns:       normaliseFX(byScenario[scn], &fb, fallbackFX, opts.Log),
		}

		specByClause := map[string]*domain.CovenantSpec{}
		for _, s := range specs {
			specByClause[s.ClauseID] = s
		}

		for _, clause := range tpl.ClausesFor(scn) {
			spec, found := specByClause[clause]
			if !found {
				rep.Missing = append(rep.Missing, fmt.Sprintf("%s/%s: no specification", scn, clause))
				continue
			}
			v, err := engine.Evaluate(spec, in)
			if err != nil {
				rep.Missing = append(rep.Missing, fmt.Sprintf("%s/%s: %v", scn, clause, err))
				opts.Log.Error("evaluation failed", "scenario", scn, "clause", clause, "err", err)
				continue
			}
			evidence, candidates := engine.Evidence(spec, in, v)
			v.EvidenceID = evidence
			if len(candidates) > 1 && evidence == nil {
				v.Trace = append(v.Trace, fmt.Sprintf(
					"leave-one-out: %d transactions each flip the verdict, so no single one is the evidence", len(candidates)))
			}
			cells = append(cells, cell{spec: spec, verdict: v, inputs: in})
			candidatesOf[store.VerdictKey(scn, clause)] = candidates
			opts.Log.Info("evaluated", "scenario", scn, "clause", clause,
				"status", v.Status, "actual", v.Actual.StringFixed(2), "evidence", evidenceText(v.EvidenceID))
		}
	}

	reviewed, err := criticise(ctx, opts, cells)
	if err != nil {
		return nil, err
	}

	for i := range cells {
		c := &cells[i]
		key := store.VerdictKey(c.spec.ScenarioID, c.spec.ClauseID)
		row := Row{
			ScenarioID: c.spec.ScenarioID, ClauseID: c.spec.ClauseID, Status: c.verdict.Status,
			Actual: c.verdict.Actual.StringFixed(2), Evidence: evidenceText(c.verdict.EvidenceID),
		}
		if c.verdict.CarveoutApplied != "" {
			row.Note = "carve-out"
		}

		r, hasReview := reviewed[key]
		if hasReview {
			rep.Reviewed++
			c.verdict.Trace = append(c.verdict.Trace, criticTrace(r))
			if !r.agrees {
				rep.Disputed++
				row.Note = strings.TrimSpace(row.Note + " " + criticNote(r))
				c.verdict.Confidence = min(c.verdict.Confidence, _criticConfidence)
			}
		}

		if !opts.DryRun {
			if err := opts.Store.PutVerdict(c.verdict); err != nil {
				return nil, err
			}
		}
		rep.Cells++
		if c.verdict.Status == domain.StatusBreach {
			rep.Breaches++
		}
		if c.verdict.EvidenceID != nil {
			rep.Evidence++
		}
		if c.verdict.Confidence <= reviewConfidence {
			if !strings.Contains(row.Note, "review") {
				row.Note = strings.TrimSpace(row.Note + " review")
			}
			rep.Flagged++
			entry := reviewEntry{spec: c.spec, verdict: c.verdict, candidates: candidatesOf[key]}
			if hasReview && !r.agrees {
				entry.concern, entry.issue = r.concern, r.issue
			}
			reviews = append(reviews, entry)
		}
		rep.Rows = append(rep.Rows, row)
	}

	if !opts.DryRun {
		rep.ReviewPath = filepath.Join(opts.Cfg.OutDir, "review.md")
		if err := writeReview(rep.ReviewPath, reviews); err != nil {
			return nil, err
		}
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

func criticTrace(r review) string {
	if r.agrees {
		return "critic: the figures follow from the clause"
	}
	return fmt.Sprintf("critic disagrees (%s): %s", r.issue, r.concern)
}

func criticNote(r review) string {
	if r.issue == "" || r.issue == "none" {
		return "disputed"
	}
	return "disputed:" + r.issue
}

func normaliseFX(txns []domain.Txn, fb *domain.FactBase, fallback map[string]decimal.Decimal, log *slog.Logger) []domain.Txn {
	out := make([]domain.Txn, 0, len(txns))
	for _, t := range txns {
		cur := strings.ToUpper(strings.TrimSpace(t.Currency))
		switch {
		case cur == "" || cur == "USD":
			t.AmountUSD = t.Amount
		default:
			rate, ok := decimal.Zero, false
			if fb != nil {
				rate, ok = fb.FXRates[cur]
			}
			if !ok || !rate.IsPositive() {
				rate, ok = fallback[cur]
			}
			if !ok || !rate.IsPositive() {

				log.Warn("no exchange rate; treating the amount as USD", "txn", t.ID, "currency", cur)
				t.AmountUSD = t.Amount
				break
			}
			t.AmountUSD = t.Amount.Mul(rate)
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b domain.Txn) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func evidenceText(id *string) string {
	if id == nil {
		return "—"
	}
	return *id
}

type reviewEntry struct {
	spec       *domain.CovenantSpec
	verdict    domain.Verdict
	candidates []string

	concern string
	issue   string
}

func writeReview(path string, entries []reviewEntry) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Cells to check by hand\n\n")
	if len(entries) == 0 {
		fmt.Fprintf(&b, "None: every cell was computed from a specification the critic accepted,\n"+
			"with every term resolved to a disclosed figure.\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "## %s / %s — %s\n\n", e.spec.ScenarioID, e.spec.ClauseID, e.spec.Title)
		fmt.Fprintf(&b, "- **verdict**: %s, actual %s, evidence %s\n",
			e.verdict.Status, e.verdict.Actual.StringFixed(2), evidenceText(e.verdict.EvidenceID))
		fmt.Fprintf(&b, "- **metric**: `%s %s %s`\n", e.spec.Expression, e.spec.Op, e.spec.Threshold)
		fmt.Fprintf(&b, "- **confidence**: %.2f\n", e.verdict.Confidence)
		if e.concern != "" {
			fmt.Fprintf(&b, "- **critic (%s)**: %s\n", e.issue, e.concern)
		}
		if len(e.candidates) > 1 {
			fmt.Fprintf(&b, "- **leave-one-out candidates**: %s\n", strings.Join(e.candidates, ", "))
		}
		if q := e.spec.SourceRef.Quote; q != "" {
			fmt.Fprintf(&b, "\n> %s\n", q)
		}
		fmt.Fprintf(&b, "\n```\n")
		for _, line := range e.verdict.Trace {
			fmt.Fprintf(&b, "%s\n", line)
		}
		fmt.Fprintf(&b, "```\n\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 104)
	fmt.Fprintf(&b, "\n%s\nEVALUATION  (%.1fs)  %d cells, %d breaches, %d with evidence, %d flagged for review\n%s\n",
		line, r.Duration.Seconds(), r.Cells, r.Breaches, r.Evidence, r.Flagged, line)
	if r.Reviewed > 0 {
		fmt.Fprintf(&b, "  critic read %d cells and disagreed with %d\n\n", r.Reviewed, r.Disputed)
	}
	fmt.Fprintf(&b, "  %-5s %-6s %-10s %18s  %-16s %s\n", "scn", "clause", "status", "actual", "evidence", "note")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "  %-5s %-6s %-10s %18s  %-16s %s\n",
			row.ScenarioID, row.ClauseID, row.Status, row.Actual, row.Evidence, row.Note)
	}
	fmt.Fprintf(&b, "%s\n", line)
	if len(r.Missing) > 0 {
		fmt.Fprintf(&b, "  UNANSWERED:\n")
		for _, m := range r.Missing {
			fmt.Fprintf(&b, "    %s\n", m)
		}
	}
	fmt.Fprintf(&b, "wrote %s\n%s\n", r.ReviewPath, line)
	return b.String()
}
