package submit

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/tidwall/gjson"
)

const (
	statusPoints   = 0.50
	actualPoints   = 0.30
	evidencePoints = 0.20
	zeroAtError    = 0.05
)

type GroundTruth struct {
	Scenarios map[string]struct {
		Covenants map[string]struct {
			Status     string   `json:"status"`
			Actual     *float64 `json:"actual"`
			EvidenceID *string  `json:"evidence_txn_id"`
		} `json:"covenants"`
	} `json:"scenarios"`
	Seed    any `json:"seed"`
	Version any `json:"version"`
}

type CellScore struct {
	ScenarioID string  `json:"scenario_id"`
	ClauseID   string  `json:"clause_id"`
	Status     float64 `json:"status_points"`
	Actual     float64 `json:"actual_points"`
	Evidence   float64 `json:"evidence_points"`
	Total      float64 `json:"total"`
	Note       string  `json:"note,omitempty"`
}

type ScoreReport struct {
	Cells         []CellScore `json:"cells"`
	Total         float64     `json:"total"`
	Max           float64     `json:"max"`
	StatusCorrect int         `json:"status_correct"`
	EvidenceExact int         `json:"evidence_exact"`
	EvidenceKeyed int         `json:"evidence_keyed"`
	MissingInKey  []string    `json:"missing_in_key,omitempty"`
}

func Score(submissionPath, groundTruthPath string, tpl *domain.Template) (*ScoreReport, error) {
	subRaw, err := os.ReadFile(submissionPath)
	if err != nil {
		return nil, fmt.Errorf("read submission: %w", err)
	}
	if !gjson.ValidBytes(subRaw) {
		return nil, fmt.Errorf("%s is not valid JSON", submissionPath)
	}
	gtRaw, err := os.ReadFile(groundTruthPath)
	if err != nil {
		return nil, fmt.Errorf("read ground truth: %w (it ships with the public dataset only)", err)
	}
	var gt GroundTruth
	if err := json.Unmarshal(gtRaw, &gt); err != nil {
		return nil, fmt.Errorf("parse ground truth: %w", err)
	}

	answers := gjson.ParseBytes(subRaw).Get("answers")
	rep := &ScoreReport{}

	for _, c := range tpl.Cells {
		scn, ok := gt.Scenarios[c.ScenarioID]
		if !ok {
			rep.MissingInKey = append(rep.MissingInKey, c.ScenarioID+"/"+c.ClauseID)
			continue
		}
		key, ok := scn.Covenants[c.ClauseID]
		if !ok {
			rep.MissingInKey = append(rep.MissingInKey, c.ScenarioID+"/"+c.ClauseID)
			continue
		}
		rep.Max += 1.0
		if key.EvidenceID != nil {
			rep.EvidenceKeyed++
		}

		cs := CellScore{ScenarioID: c.ScenarioID, ClauseID: c.ClauseID}
		cell := answers.Get(EscapeKey(c.ScenarioID) + "." + EscapeKey(c.ClauseID))
		if !cell.Exists() {
			cs.Note = "cell missing"
			rep.Cells = append(rep.Cells, cs)
			continue
		}

		if cell.Get("status").String() != key.Status {
			cs.Note = fmt.Sprintf("status %s, key %s", cell.Get("status").String(), key.Status)
			rep.Cells = append(rep.Cells, cs)
			continue
		}
		cs.Status = statusPoints
		rep.StatusCorrect++

		actualFrac := 0.0
		got := cell.Get("actual")
		if got.Type == gjson.Number && key.Actual != nil {
			actualFrac = scaledFraction(got.Float(), *key.Actual)
		}
		cs.Actual = actualPoints * actualFrac

		if key.EvidenceID != nil {
			ev := cell.Get("evidence_txn_id")
			if ev.Type == gjson.String && ev.String() == *key.EvidenceID {
				cs.Evidence = evidencePoints
				rep.EvidenceExact++
			} else {
				cs.Note = "evidence expected " + *key.EvidenceID
			}
		} else {

			cs.Evidence = evidencePoints * actualFrac
		}

		cs.Total = cs.Status + cs.Actual + cs.Evidence
		rep.Total += cs.Total
		rep.Cells = append(rep.Cells, cs)
	}
	return rep, nil
}

func scaledFraction(got, want float64) float64 {
	if want == 0 {
		if got == 0 {
			return 1
		}
		return 0
	}
	e := math.Abs(got-want) / math.Abs(want)
	f := 1 - e/zeroAtError
	if f < 0 {
		return 0
	}
	return f
}

func (r *ScoreReport) String() string {
	var b strings.Builder
	line := strings.Repeat("─", 62)
	fmt.Fprintf(&b, "\n%s\nLOCAL SCORE (unweighted; official score weights cells by difficulty)\n%s\n", line, line)
	fmt.Fprintf(&b, "  %-5s %-5s %7s %7s %7s %7s  %s\n", "scn", "claus", "status", "actual", "evid", "total", "note")
	for _, c := range r.Cells {
		fmt.Fprintf(&b, "  %-5s %-5s %7.2f %7.2f %7.2f %7.2f  %s\n",
			c.ScenarioID, c.ClauseID, c.Status, c.Actual, c.Evidence, c.Total, c.Note)
	}
	fmt.Fprintf(&b, "%s\n", line)
	pct := 0.0
	if r.Max > 0 {
		pct = 100 * r.Total / r.Max
	}
	fmt.Fprintf(&b, "  total          %.3f / %.0f  (%.1f%%)\n", r.Total, r.Max, pct)
	fmt.Fprintf(&b, "  status correct %d / %.0f\n", r.StatusCorrect, r.Max)
	fmt.Fprintf(&b, "  evidence exact %d / %d cells whose key has an id\n", r.EvidenceExact, r.EvidenceKeyed)
	if len(r.MissingInKey) > 0 {
		fmt.Fprintf(&b, "  not in key     %s\n", strings.Join(r.MissingInKey, " "))
	}
	fmt.Fprintf(&b, "%s\n", line)
	return b.String()
}
