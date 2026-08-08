package submit

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/tidwall/gjson"
)

// Поля ячейки шаблона: ровно эти три, ни больше ни меньше.
var _cellFields = []string{"status", "actual", "evidence_txn_id"}

type Problem struct {
	Where  string `json:"where"`
	Detail string `json:"detail"`
}

func (p Problem) String() string { return p.Where + ": " + p.Detail }

type ValidationReport struct {
	SubmissionPath string    `json:"submission_path"`
	Cells          int       `json:"cells"`
	Problems       []Problem `json:"problems,omitempty"`
}

func (r *ValidationReport) OK() bool { return len(r.Problems) == 0 }

func (r *ValidationReport) String() string {
	var b strings.Builder
	if r.OK() {
		fmt.Fprintf(&b, "VALID  %s\n", r.SubmissionPath)
		fmt.Fprintf(&b, "  %d cells, structure identical to the template, all values in range\n", r.Cells)
		return b.String()
	}
	fmt.Fprintf(&b, "INVALID  %s\n", r.SubmissionPath)
	fmt.Fprintf(&b, "  %d problems:\n", len(r.Problems))
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "    %s\n", p)
	}
	return b.String()
}

type LedgerIndex map[string]string

func Validate(submissionPath string, tpl *domain.Template, ledger LedgerIndex) (*ValidationReport, error) {
	raw, err := os.ReadFile(submissionPath)
	if err != nil {
		return nil, fmt.Errorf("read submission: %w (run `halyk-agent submit` first)", err)
	}
	if !gjson.ValidBytes(raw) {
		return &ValidationReport{
			SubmissionPath: submissionPath,
			Problems:       []Problem{{Where: "<file>", Detail: "not valid JSON; every cell is unscoreable"}},
		}, nil
	}

	rep := &ValidationReport{SubmissionPath: submissionPath, Cells: len(tpl.Cells)}
	doc := gjson.ParseBytes(raw)

	for _, key := range []string{"team", "contact_email", "model"} {
		v := doc.Get(key)
		if !v.Exists() {
			rep.Problems = append(rep.Problems, Problem{key, "missing"})
		} else if strings.TrimSpace(v.String()) == "" {
			rep.Problems = append(rep.Problems, Problem{key, "empty"})
		}
	}

	answers := doc.Get("answers")
	if !answers.Exists() || !answers.IsObject() {
		rep.Problems = append(rep.Problems, Problem{"answers", "missing or not an object"})
		return rep, nil
	}

	wantScenarios := make(map[string]map[string]bool)
	for _, c := range tpl.Cells {
		if wantScenarios[c.ScenarioID] == nil {
			wantScenarios[c.ScenarioID] = make(map[string]bool)
		}
		wantScenarios[c.ScenarioID][c.ClauseID] = true
	}
	gotScenarios := make(map[string]map[string]bool)
	answers.ForEach(func(scn, clauses gjson.Result) bool {
		gotScenarios[scn.String()] = make(map[string]bool)
		if !clauses.IsObject() {
			rep.Problems = append(rep.Problems, Problem{"answers." + scn.String(), "not an object"})
			return true
		}
		clauses.ForEach(func(clause, _ gjson.Result) bool {
			gotScenarios[scn.String()][clause.String()] = true
			return true
		})
		return true
	})
	for _, scn := range domain.SortedKeys(wantScenarios) {
		got, ok := gotScenarios[scn]
		if !ok {
			rep.Problems = append(rep.Problems, Problem{"answers." + scn, "scenario missing"})
			continue
		}
		for _, clause := range domain.SortedKeys(wantScenarios[scn]) {
			if !got[clause] {
				rep.Problems = append(rep.Problems, Problem{"answers." + scn + "." + clause, "cell missing"})
			}
		}
		for _, clause := range domain.SortedKeys(got) {
			if !wantScenarios[scn][clause] {
				rep.Problems = append(rep.Problems, Problem{"answers." + scn + "." + clause, "cell is not in the template"})
			}
		}
	}
	for _, scn := range domain.SortedKeys(gotScenarios) {
		if _, ok := wantScenarios[scn]; !ok {
			rep.Problems = append(rep.Problems, Problem{"answers." + scn, "scenario is not in the template"})
		}
	}

	for _, c := range tpl.Cells {
		where := "answers." + c.ScenarioID + "." + c.ClauseID
		cell := answers.Get(EscapeKey(c.ScenarioID) + "." + EscapeKey(c.ClauseID))
		if !cell.Exists() {
			continue
		}
		validateCell(rep, where, c.ScenarioID, cell, ledger)
	}
	return rep, nil
}

func validateCell(rep *ValidationReport, where, scenarioID string, cell gjson.Result, ledger LedgerIndex) {
	cell.ForEach(func(k, _ gjson.Result) bool {
		if !slices.Contains(_cellFields, k.String()) {
			rep.Problems = append(rep.Problems, Problem{where + "." + k.String(), "field is not in the template"})
		}
		return true
	})

	status := cell.Get("status")
	switch {
	case !status.Exists() || status.Type == gjson.Null:
		rep.Problems = append(rep.Problems, Problem{where + ".status", "not filled"})
	case status.Type != gjson.String:
		rep.Problems = append(rep.Problems, Problem{where + ".status", "must be a string"})
	case status.String() != domain.StatusCompliant && status.String() != domain.StatusBreach:
		rep.Problems = append(rep.Problems, Problem{where + ".status",
			fmt.Sprintf("%q is not exactly %s or %s", status.String(), domain.StatusCompliant, domain.StatusBreach)})
	}

	actual := cell.Get("actual")
	switch {
	case !actual.Exists() || actual.Type == gjson.Null:
		rep.Problems = append(rep.Problems, Problem{where + ".actual", "not filled"})
	case actual.Type != gjson.Number:
		rep.Problems = append(rep.Problems, Problem{where + ".actual",
			fmt.Sprintf("must be a JSON number, got %s", actual.Type)})
	case actual.Float() <= 0:
		rep.Problems = append(rep.Problems, Problem{where + ".actual",
			fmt.Sprintf("must be positive, got %s", actual.Raw)})
	case math.IsNaN(actual.Float()) || math.IsInf(actual.Float(), 0):
		rep.Problems = append(rep.Problems, Problem{where + ".actual", "must be finite"})
	default:
		if decimals(actual.Raw) > 2 {
			rep.Problems = append(rep.Problems, Problem{where + ".actual",
				fmt.Sprintf("more than two decimal places: %s", actual.Raw)})
		}
	}

	evidence := cell.Get("evidence_txn_id")
	switch {
	case !evidence.Exists():
		rep.Problems = append(rep.Problems, Problem{where + ".evidence_txn_id", "key missing"})
	case evidence.Type == gjson.Null:

	case evidence.Type != gjson.String:
		rep.Problems = append(rep.Problems, Problem{where + ".evidence_txn_id", "must be a string or null"})
	default:
		id := evidence.String()
		owner, ok := ledger[id]
		if !ok {
			rep.Problems = append(rep.Problems, Problem{where + ".evidence_txn_id",
				fmt.Sprintf("%q does not exist in the ledger", id)})
		} else if owner != scenarioID {
			rep.Problems = append(rep.Problems, Problem{where + ".evidence_txn_id",
				fmt.Sprintf("%q belongs to %s, not %s", id, owner, scenarioID)})
		}
	}
}

func decimals(raw string) int {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexAny(raw, "eE"); i >= 0 {

		return 0
	}
	dot := strings.IndexByte(raw, '.')
	if dot < 0 {
		return 0
	}
	return len(raw) - dot - 1
}

func LedgerIndexFromTxns(txns []domain.Txn) LedgerIndex {
	idx := make(LedgerIndex, len(txns))
	for _, t := range txns {
		idx[t.ID] = t.ScenarioID
	}
	return idx
}

func (r *ValidationReport) MarshalIndent() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
