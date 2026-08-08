package submit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSubmission(t *testing.T, body string) string {
	t.Helper()
	return writeTemp(t, "submission.json", body)
}

var _testLedger = LedgerIndex{
	"TXN-P1-0001": "P1",
	"TXN-B4-0020": "B4",
}

const _goodSubmission = `{
  "team": "team-x",
  "contact_email": "a@b.io",
  "model": "gemini-2.5-pro",
  "answers": {
    "P1": {
      "6.1": {"status": "BREACH", "actual": 1850000.50, "evidence_txn_id": "TXN-P1-0001"},
      "6.2": {"status": "COMPLIANT", "actual": 1.68, "evidence_txn_id": null}
    },
    "B4": {
      "6.3": {"status": "COMPLIANT", "actual": 300000.00, "evidence_txn_id": null}
    }
  }
}`

func TestValidateAcceptsAGoodSubmission(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	rep, err := Validate(writeSubmission(t, _goodSubmission), tpl, _testLedger)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("expected a valid submission, got problems: %v", rep.Problems)
	}
	if rep.Cells != 3 {
		t.Errorf("cells = %d, want 3", rep.Cells)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "broken json",
			body: `{"answers":`,
			want: "not valid JSON",
		},
		{
			name: "empty team",
			body: strings.Replace(_goodSubmission, `"team": "team-x"`, `"team": ""`, 1),
			want: "team: empty",
		},
		{
			name: "unfilled status",
			body: strings.Replace(_goodSubmission, `"status": "COMPLIANT", "actual": 1.68`, `"status": null, "actual": 1.68`, 1),
			want: "status: not filled",
		},
		{
			name: "lowercase status",
			body: strings.Replace(_goodSubmission, `"status": "BREACH"`, `"status": "breach"`, 1),
			want: "is not exactly COMPLIANT or BREACH",
		},
		{
			name: "actual as string",
			body: strings.Replace(_goodSubmission, `"actual": 1.68`, `"actual": "1.68"`, 1),
			want: "must be a JSON number",
		},
		{
			name: "actual null",
			body: strings.Replace(_goodSubmission, `"actual": 1.68`, `"actual": null`, 1),
			want: "actual: not filled",
		},
		{
			name: "actual negative",
			body: strings.Replace(_goodSubmission, `"actual": 1.68`, `"actual": -1.68`, 1),
			want: "must be positive",
		},
		{
			name: "actual zero",
			body: strings.Replace(_goodSubmission, `"actual": 1.68`, `"actual": 0`, 1),
			want: "must be positive",
		},
		{
			name: "three decimals",
			body: strings.Replace(_goodSubmission, `"actual": 1.68`, `"actual": 1.685`, 1),
			want: "more than two decimal places",
		},
		{
			name: "unknown evidence id",
			body: strings.Replace(_goodSubmission, `"TXN-P1-0001"`, `"TXN-P1-9999"`, 1),
			want: "does not exist in the ledger",
		},
		{
			name: "evidence from another borrower",
			body: strings.Replace(_goodSubmission, `"TXN-P1-0001"`, `"TXN-B4-0020"`, 1),
			want: "belongs to B4, not P1",
		},
		{
			name: "evidence not a string",
			body: strings.Replace(_goodSubmission, `"evidence_txn_id": null`, `"evidence_txn_id": 42`, 1),
			want: "must be a string or null",
		},
		{
			name: "cell missing",
			body: strings.Replace(_goodSubmission,
				`"6.2": {"status": "COMPLIANT", "actual": 1.68, "evidence_txn_id": null}`, `"6.9": {}`, 1),
			want: "answers.P1.6.2: cell missing",
		},
		{
			name: "extra scenario",
			body: strings.Replace(_goodSubmission, `"B4": {`, `"B9": {"6.1": {"status":"BREACH","actual":1,"evidence_txn_id":null}}, "B4": {`, 1),
			want: "answers.B9: scenario is not in the template",
		},
		{
			name: "extra field in cell",
			body: strings.Replace(_goodSubmission, `"actual": 1.68,`, `"actual": 1.68, "note": "x",`, 1),
			want: "field is not in the template",
		},
	}

	tpl := writeTemplate(t, _templateJSON)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := Validate(writeSubmission(t, tt.body), tpl, _testLedger)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if rep.OK() {
				t.Fatalf("expected a problem containing %q, got a clean report", tt.want)
			}
			joined := make([]string, 0, len(rep.Problems))
			for _, p := range rep.Problems {
				joined = append(joined, p.String())
			}
			all := strings.Join(joined, "\n")
			if !strings.Contains(all, tt.want) {
				t.Errorf("problems:\n%s\nwant one containing %q", all, tt.want)
			}
		})
	}
}

func TestValidateMissingFile(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	_, err := Validate(filepath.Join(t.TempDir(), "nope.json"), tpl, _testLedger)
	if err == nil {
		t.Fatal("expected an error for a missing submission file")
	}
	if !strings.Contains(err.Error(), "halyk-agent submit") {
		t.Errorf("the error should say how to produce the file, got: %v", err)
	}
}

func TestDecimals(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"1", 0}, {"1.5", 1}, {"1.55", 2}, {"1.555", 3},
		{"300000.00", 2}, {"1e5", 0}, {"-1.5", 1},
	}
	for _, tt := range tests {
		if got := decimals(tt.in); got != tt.want {
			t.Errorf("decimals(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
