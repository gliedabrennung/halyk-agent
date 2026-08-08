package stability

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/classify"
	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/covenants"
	"github.com/gliedabrennung/halyk-agent/internal/evaluate"
	"github.com/gliedabrennung/halyk-agent/internal/facts"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
)

var _actualTolerance = decimal.RequireFromString("0.005")

type Options struct {
	Cfg   *config.Config
	Store *store.Store
	Log   *slog.Logger

	Passes int
	Only   []string
}

type Report struct {
	Duration time.Duration `json:"duration"`
	Passes   int           `json:"passes"`
	Cells    int           `json:"cells"`

	Stable int   `json:"stable"`
	Rows   []Row `json:"rows"`

	Aborted string `json:"aborted,omitempty"`
}

type Row struct {
	ScenarioID string   `json:"scenario_id"`
	ClauseID   string   `json:"clause_id"`
	Baseline   string   `json:"baseline"`
	Passes     []string `json:"passes"`
	What       string   `json:"what"`
}

func (r *Report) OK() bool { return r.Stable == r.Cells }

type answer struct {
	status   string
	actual   decimal.Decimal
	evidence string
}

func (a answer) String() string {
	return fmt.Sprintf("%s %s %s", a.status, a.actual.StringFixed(2), a.evidence)
}

func Run(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()
	if opts.Passes < 1 {
		opts.Passes = 1
	}

	baseline, err := answers(opts.Store)
	if err != nil {
		return nil, err
	}
	if len(baseline) == 0 {
		return nil, fmt.Errorf("no verdicts to compare against; run `halyk-agent evaluate` first")
	}

	rep := &Report{Passes: opts.Passes, Cells: len(baseline)}
	moved := make(map[string][]string)
	what := make(map[string][]string)
	addWhat := func(key, w string) {
		if !slices.Contains(what[key], w) {
			what[key] = append(what[key], w)
		}
	}

	for pass := 1; pass <= opts.Passes; pass++ {
		ns := fmt.Sprintf(":probe%d", pass)
		opts.Log.Info("stability pass start", "pass", pass, "of", opts.Passes, "namespace", ns)

		got, err := onePass(ctx, opts, ns)
		if err != nil {
			if llm.IsQuotaExhausted(err) {
				rep.Aborted = fmt.Sprintf("pass %d stopped: the model's daily quota is exhausted", pass)
				rep.Passes = pass - 1
				opts.Log.Error("stability probe stopped", "reason", "daily quota exhausted", "completed_passes", rep.Passes)
				break
			}
			return nil, err
		}

		for key, base := range baseline {
			g, ok := got[key]
			if !ok {
				moved[key] = append(moved[key], "not answered")
				addWhat(key, "missing")
				continue
			}
			diff := differences(base, g)
			if len(diff) == 0 {
				continue
			}
			moved[key] = append(moved[key], g.String())
			for _, d := range diff {
				addWhat(key, d)
			}
		}
	}

	for key := range baseline {
		if len(moved[key]) == 0 {
			rep.Stable++
			continue
		}
		scn, clause, _ := strings.Cut(key, "/")
		rep.Rows = append(rep.Rows, Row{
			ScenarioID: scn, ClauseID: clause,
			Baseline: baseline[key].String(),
			Passes:   moved[key],
			What:     joinWhat(what[key]),
		})
	}
	slices.SortFunc(rep.Rows, func(a, b Row) int {
		return cmp.Or(
			cmp.Compare(a.ScenarioID, b.ScenarioID),
			cmp.Compare(a.ClauseID, b.ClauseID))
	})
	rep.Duration = time.Since(start)
	return rep, nil
}

func onePass(ctx context.Context, opts Options, ns string) (map[string]answer, error) {
	client := llm.NewWithNonce(opts.Cfg, opts.Store, opts.Log, ns)

	cov, err := covenants.Run(ctx, covenants.Options{
		Cfg: opts.Cfg, Store: opts.Store, Log: opts.Log, Client: client,
		Only: opts.Only, Namespace: ns,
	})
	if err != nil {
		return nil, fmt.Errorf("covenants: %w", err)
	}
	if !cov.OK() {
		return nil, fmt.Errorf("covenants: %s", strings.Join(cov.Failed, "; "))
	}
	fb, err := facts.Run(ctx, facts.Options{
		Cfg: opts.Cfg, Store: opts.Store, Log: opts.Log, Client: client,
		Only: opts.Only, Namespace: ns,
	})
	if err != nil {
		return nil, fmt.Errorf("facts: %w", err)
	}
	if !fb.OK() {
		return nil, fmt.Errorf("facts: %s", strings.Join(fb.Failed, "; "))
	}
	if _, err := classify.Run(ctx, classify.Options{
		Cfg: opts.Cfg, Store: opts.Store, Log: opts.Log, Client: client,
		Only: opts.Only, Namespace: ns, FactsNamespace: ns,
	}); err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	rep, err := evaluate.Run(ctx, evaluate.Options{
		Cfg: opts.Cfg, Store: opts.Store, Log: opts.Log,
		Only: opts.Only, Namespace: ns, DryRun: true,
	})
	if err != nil {
		return nil, fmt.Errorf("evaluate: %w", err)
	}

	out := make(map[string]answer)
	for _, row := range rep.Rows {
		actual, err := decimal.NewFromString(row.Actual)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: unreadable actual %q", row.ScenarioID, row.ClauseID, row.Actual)
		}
		out[store.VerdictKey(row.ScenarioID, row.ClauseID)] = answer{
			status: row.Status, actual: actual, evidence: row.Evidence,
		}
	}
	return out, nil
}

func answers(st *store.Store) (map[string]answer, error) {
	verdicts, err := st.LoadVerdicts()
	if err != nil {
		return nil, err
	}
	out := make(map[string]answer, len(verdicts))
	for key, v := range verdicts {
		a := answer{status: v.Status, actual: v.Actual, evidence: "—"}
		if v.EvidenceID != nil {
			a.evidence = *v.EvidenceID
		}
		out[key] = a
	}
	return out, nil
}

func differences(a, b answer) []string {
	var out []string
	if a.status != b.status {
		out = append(out, "status")
	}
	if !within(a.actual, b.actual) {
		out = append(out, "actual")
	}
	if a.evidence != b.evidence {
		out = append(out, "evidence")
	}
	return out
}

func within(a, b decimal.Decimal) bool {
	if a.Equal(b) {
		return true
	}
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.Sub(b).Abs().Div(a.Abs()).LessThanOrEqual(_actualTolerance)
}

// joinWhat печатает виды расхождений в фиксированном порядке, а не в порядке появления.
func joinWhat(set []string) string {
	var out []string
	for _, k := range []string{"status", "actual", "evidence", "missing"} {
		if slices.Contains(set, k) {
			out = append(out, k)
		}
	}
	return strings.Join(out, "+")
}

func (r *Report) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 104)
	fmt.Fprintf(&b, "\n%s\nSTABILITY  (%.0fs)  %d independent pass(es) over %d cells\n%s\n",
		line, r.Duration.Seconds(), r.Passes, r.Cells, line)
	if r.Passes == 0 {
		fmt.Fprintf(&b, "  no pass completed\n")
	} else {
		fmt.Fprintf(&b, "  %d of %d cells came out identical every time (%.0f%%)\n\n",
			r.Stable, r.Cells, 100*float64(r.Stable)/float64(r.Cells))
	}
	if len(r.Rows) > 0 {
		fmt.Fprintf(&b, "  %-5s %-6s %-10s %-34s %s\n", "scn", "clause", "moved", "answer in hand", "what the passes gave")
		for _, row := range r.Rows {
			fmt.Fprintf(&b, "  %-5s %-6s %-10s %-34s %s\n",
				row.ScenarioID, row.ClauseID, row.What, row.Baseline, strings.Join(row.Passes, " | "))
		}
	}
	fmt.Fprintf(&b, "%s\n", line)
	if r.Aborted != "" {
		fmt.Fprintf(&b, "  %s\n%s\n", r.Aborted, line)
	}
	return b.String()
}
