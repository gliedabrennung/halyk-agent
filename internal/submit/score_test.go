package submit

import (
	"math"
	"testing"
)

const _groundTruthJSON = `{
  "scenarios": {
    "P1": {"covenants": {
      "6.1": {"status": "BREACH",    "actual": 1850000.50, "evidence_txn_id": "TXN-P1-0001"},
      "6.2": {"status": "COMPLIANT", "actual": 1.68,       "evidence_txn_id": null}
    }},
    "B4": {"covenants": {
      "6.3": {"status": "COMPLIANT", "actual": 300000.00,  "evidence_txn_id": null}
    }}
  },
  "seed": 1, "version": "1"
}`

func writeGroundTruth(t *testing.T) string {
	t.Helper()
	return writeTemp(t, "ground_truth.json", _groundTruthJSON)
}

func scoreOf(t *testing.T, submission string) *ScoreReport {
	t.Helper()
	tpl := writeTemplate(t, _templateJSON)
	rep, err := Score(writeSubmission(t, submission), writeGroundTruth(t), tpl)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	return rep
}

func cell(t *testing.T, rep *ScoreReport, scn, clause string) CellScore {
	t.Helper()
	for _, c := range rep.Cells {
		if c.ScenarioID == scn && c.ClauseID == clause {
			return c
		}
	}
	t.Fatalf("cell %s/%s not scored", scn, clause)
	return CellScore{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScorePerfectSubmission(t *testing.T) {
	rep := scoreOf(t, _goodSubmission)
	if !near(rep.Total, 3.0) {
		t.Errorf("total = %v, want 3.0 for three perfect cells", rep.Total)
	}
	if rep.StatusCorrect != 3 {
		t.Errorf("status correct = %d, want 3", rep.StatusCorrect)
	}
	if rep.EvidenceExact != 1 || rep.EvidenceKeyed != 1 {
		t.Errorf("evidence exact/keyed = %d/%d, want 1/1", rep.EvidenceExact, rep.EvidenceKeyed)
	}
}

func TestScoreWrongStatusZeroesTheCell(t *testing.T) {
	sub := `{"team":"t","contact_email":"e","model":"m","answers":{
      "P1":{"6.1":{"status":"COMPLIANT","actual":1850000.50,"evidence_txn_id":"TXN-P1-0001"},
            "6.2":{"status":"COMPLIANT","actual":1.68,"evidence_txn_id":null}},
      "B4":{"6.3":{"status":"COMPLIANT","actual":300000.00,"evidence_txn_id":null}}}}`
	c := cell(t, scoreOf(t, sub), "P1", "6.1")
	if !near(c.Total, 0) {
		t.Errorf("cell total = %v, want 0 despite a perfect actual and evidence", c.Total)
	}
}

func TestScoreActualScale(t *testing.T) {
	tests := []struct {
		name        string
		actual      string
		wantActual  float64
		wantEvidenc float64
	}{
		{"exact", "300000.00", 0.30, 0.20},
		{"2.5% low", "292500.00", 0.15, 0.10},
		{"5% low", "285000.00", 0.00, 0.00},
		{"10% high", "330000.00", 0.00, 0.00},
		{"rounding noise", "300000.01", 0.30, 0.20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := `{"team":"t","contact_email":"e","model":"m","answers":{
              "P1":{"6.1":{"status":"BREACH","actual":1850000.50,"evidence_txn_id":"TXN-P1-0001"},
                    "6.2":{"status":"COMPLIANT","actual":1.68,"evidence_txn_id":null}},
              "B4":{"6.3":{"status":"COMPLIANT","actual":` + tt.actual + `,"evidence_txn_id":null}}}}`
			c := cell(t, scoreOf(t, sub), "B4", "6.3")
			if math.Abs(c.Actual-tt.wantActual) > 0.001 {
				t.Errorf("actual points = %.4f, want %.2f", c.Actual, tt.wantActual)
			}
			if math.Abs(c.Evidence-tt.wantEvidenc) > 0.001 {
				t.Errorf("evidence points = %.4f, want %.2f (they ride on actual when the key is null)",
					c.Evidence, tt.wantEvidenc)
			}
		})
	}
}

func TestScoreEvidenceIsAllOrNothing(t *testing.T) {
	for _, tt := range []struct {
		evidence string
		want     float64
	}{
		{`"TXN-P1-0001"`, 0.20},
		{`"TXN-P1-0002"`, 0.00},
		{`null`, 0.00},
	} {
		sub := `{"team":"t","contact_email":"e","model":"m","answers":{
          "P1":{"6.1":{"status":"BREACH","actual":1850000.50,"evidence_txn_id":` + tt.evidence + `},
                "6.2":{"status":"COMPLIANT","actual":1.68,"evidence_txn_id":null}},
          "B4":{"6.3":{"status":"COMPLIANT","actual":300000.00,"evidence_txn_id":null}}}}`
		c := cell(t, scoreOf(t, sub), "P1", "6.1")
		if !near(c.Evidence, tt.want) {
			t.Errorf("evidence %s scored %.2f, want %.2f", tt.evidence, c.Evidence, tt.want)
		}
	}
}

func TestScoreNonNumericActual(t *testing.T) {
	sub := `{"team":"t","contact_email":"e","model":"m","answers":{
      "P1":{"6.1":{"status":"BREACH","actual":1850000.50,"evidence_txn_id":"TXN-P1-0001"},
            "6.2":{"status":"COMPLIANT","actual":1.68,"evidence_txn_id":null}},
      "B4":{"6.3":{"status":"COMPLIANT","actual":"300000.00","evidence_txn_id":null}}}}`
	c := cell(t, scoreOf(t, sub), "B4", "6.3")
	if !near(c.Status, 0.50) || !near(c.Actual, 0) || !near(c.Evidence, 0) {
		t.Errorf("got %.2f/%.2f/%.2f, want 0.50/0.00/0.00", c.Status, c.Actual, c.Evidence)
	}
}

func TestScaledFraction(t *testing.T) {
	tests := []struct {
		got, want, expect float64
	}{
		{100, 100, 1},
		{102.5, 100, 0.5},
		{97.5, 100, 0.5},
		{105, 100, 0},
		{0, 100, 0},
		{0, 0, 1},
		{1, 0, 0},
	}
	for _, tt := range tests {
		if got := scaledFraction(tt.got, tt.want); math.Abs(got-tt.expect) > 1e-9 {
			t.Errorf("scaledFraction(%v, %v) = %v, want %v", tt.got, tt.want, got, tt.expect)
		}
	}
}
