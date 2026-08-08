package submit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

const _templateJSON = `{
  "team": "",
  "contact_email": "",
  "model": "",
  "answers": {
    "P1": {
      "6.1": {"status": null, "actual": null, "evidence_txn_id": null},
      "6.2": {"status": null, "actual": null, "evidence_txn_id": null}
    },
    "B4": {
      "6.3": {"status": null, "actual": null, "evidence_txn_id": null}
    }
  }
}`

func writeTemplate(t *testing.T, body string) *domain.Template {
	t.Helper()
	path := filepath.Join(t.TempDir(), "submission_template.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tpl, err := ingest.ParseTemplate(path)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	return tpl
}

func testConfig() *config.Config {
	return &config.Config{Team: "team-x", ContactEmail: "a@b.io", Model: "gemini-2.5-pro"}
}

func str(s string) *string { return &s }

func TestFillWritesEveryCellInPlace(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	verdicts := []domain.Verdict{
		{ScenarioID: "P1", ClauseID: "6.1", Status: domain.StatusBreach,
			Actual: decimal.RequireFromString("1850000.5"), EvidenceID: nil},
		{ScenarioID: "P1", ClauseID: "6.2", Status: domain.StatusCompliant,
			Actual: decimal.RequireFromString("1.68"), EvidenceID: nil},
		{ScenarioID: "B4", ClauseID: "6.3", Status: domain.StatusBreach,
			Actual: decimal.RequireFromString("300000"), EvidenceID: str("TXN-B4-0020")},
	}

	raw, err := fill(tpl, verdicts, testConfig())
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	doc := gjson.ParseBytes(raw)

	if got := doc.Get("team").String(); got != "team-x" {
		t.Errorf("team = %q", got)
	}
	if got := doc.Get("contact_email").String(); got != "a@b.io" {
		t.Errorf("contact_email = %q", got)
	}
	if got := doc.Get(`answers.P1.6\.1.status`).String(); got != domain.StatusBreach {
		t.Errorf("P1/6.1 status = %q", got)
	}
	if got := doc.Get(`answers.P1.6\.2.actual`).Float(); got != 1.68 {
		t.Errorf("P1/6.2 actual = %v", got)
	}
	if got := doc.Get(`answers.B4.6\.3.evidence_txn_id`).String(); got != "TXN-B4-0020" {
		t.Errorf("B4/6.3 evidence = %q", got)
	}
	if got := doc.Get(`answers.P1.6\.1.evidence_txn_id`); got.Type != gjson.Null {
		t.Errorf("a nil evidence must be written as JSON null, got %s", got.Raw)
	}

	if n := len(doc.Get("answers.P1").Map()); n != 2 {
		t.Errorf("answers.P1 has %d keys, want 2 (dot in clause id was treated as a path separator)", n)
	}
	if doc.Get("answers.P1.6").Exists() {
		t.Error(`a key "6" appeared: the clause id was split on its dot`)
	}
}

func TestFillWritesExactlyTwoDecimals(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	verdicts := []domain.Verdict{
		{ScenarioID: "P1", ClauseID: "6.1", Status: domain.StatusCompliant, Actual: decimal.RequireFromString("300000")},
		{ScenarioID: "P1", ClauseID: "6.2", Status: domain.StatusCompliant, Actual: decimal.RequireFromString("1850000.555")},
		{ScenarioID: "B4", ClauseID: "6.3", Status: domain.StatusCompliant, Actual: decimal.RequireFromString("0.1")},
	}
	raw, err := fill(tpl, verdicts, testConfig())
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	for path, want := range map[string]string{
		`answers.P1.6\.1.actual`: "300000.00",
		`answers.P1.6\.2.actual`: "1850000.56",
		`answers.B4.6\.3.actual`: "0.10",
	} {
		if got := gjson.ParseBytes(raw).Get(path).Raw; got != want {
			t.Errorf("%s raw = %s, want %s", path, got, want)
		}
	}
}

func TestFillWritesAbsoluteValue(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	verdicts := []domain.Verdict{
		{ScenarioID: "P1", ClauseID: "6.1", Status: domain.StatusBreach, Actual: decimal.RequireFromString("-5400000.25")},
	}
	raw, err := fill(tpl, verdicts, testConfig())
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if got := gjson.ParseBytes(raw).Get(`answers.P1.6\.1.actual`).Raw; got != "5400000.25" {
		t.Errorf("actual = %s, want the absolute value 5400000.25", got)
	}
}

func TestFillRejectsInvalidStatus(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	for _, bad := range []string{"", "compliant", "Compliant", "OK", "BREACHED"} {
		_, err := fill(tpl, []domain.Verdict{
			{ScenarioID: "P1", ClauseID: "6.1", Status: bad, Actual: decimal.NewFromInt(1)},
		}, testConfig())
		if err == nil {
			t.Errorf("status %q was accepted; only the two exact strings are scoreable", bad)
		}
	}
}

func TestEscapeKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"6.1", `6\.1`},
		{"P1", "P1"},
		{"a.b.c", `a\.b\.c`},
		{"x*y", `x\*y`},
		{"q?", `q\?`},
	}
	for _, tt := range tests {
		if got := EscapeKey(tt.in); got != tt.want {
			t.Errorf("EscapeKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFillPreservesKeyOrder(t *testing.T) {
	tpl := writeTemplate(t, _templateJSON)
	raw, err := fill(tpl, []domain.Verdict{
		{ScenarioID: "B4", ClauseID: "6.3", Status: domain.StatusCompliant, Actual: decimal.NewFromInt(5)},
	}, testConfig())
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	out := string(raw)
	if strings.Index(out, `"P1"`) > strings.Index(out, `"B4"`) {
		t.Error("scenario order changed: P1 must still precede B4")
	}
	if strings.Index(out, `"team"`) > strings.Index(out, `"answers"`) {
		t.Error("top-level order changed")
	}
}
