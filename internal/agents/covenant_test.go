package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

var _testInput = CovenantInput{ScenarioID: "P1", ClauseID: "6.1", Company: "Aktau Port Services JSC"}

const _goodCovenantJSON = `{
  "clause_id": "6.1",
  "title": "Maximum Capital Intensity Ratio",
  "expression": "capex / (opex + rent)",
  "terms": [
    {"name": "capex", "kind": "statement_line", "line": "Капитальные затраты", "reclassification": "both"},
    {"name": "opex",  "kind": "statement_line", "line": "Операционные расходы", "reclassification": "both"},
    {"name": "rent",  "kind": "statement_line", "line": "Арендные платежи", "reclassification": "both"}
  ],
  "op": "<=",
  "threshold": "0.42",
  "unit": "ratio",
  "period": {"kind": "fiscal_year", "from": "2025-01-01", "to": "2025-12-31"},
  "trigger": null,
  "carveouts": [],
  "evidence_kind": "ratio",
  "quote": "не допускать, чтобы коэффициент капиталоёмкости превышал 0.42x",
  "confidence": 0.95
}`

func TestParseCovenant(t *testing.T) {
	spec, err := parseCovenant(_goodCovenantJSON, _testInput)
	if err != nil {
		t.Fatalf("parseCovenant: %v", err)
	}
	if spec.Expression != "capex / (opex + rent)" {
		t.Errorf("expression = %q", spec.Expression)
	}
	if spec.Op != "<=" {
		t.Errorf("op = %q", spec.Op)
	}
	if !spec.Threshold.Equal(mustDec("0.42")) {
		t.Errorf("threshold = %s, want 0.42", spec.Threshold)
	}
	if len(spec.Terms) != 3 {
		t.Fatalf("terms = %d, want 3", len(spec.Terms))
	}
	term, ok := spec.TermByName("capex")
	if !ok || term.Kind != domain.TermStatementLine || term.Reclassification != domain.ReclassBoth {
		t.Errorf("capex term = %+v", term)
	}
	if spec.Period.From.Format("2006-01-02") != "2025-01-01" ||
		spec.Period.To.Format("2006-01-02") != "2025-12-31" {
		t.Errorf("period = %v..%v", spec.Period.From, spec.Period.To)
	}
	if spec.ScenarioID != "P1" || spec.ClauseID != "6.1" {
		t.Errorf("the cell identity must come from the caller, not the model: %s/%s", spec.ScenarioID, spec.ClauseID)
	}
}

func TestParseCovenantAcceptsAMarkdownFence(t *testing.T) {
	if _, err := parseCovenant("```json\n"+_goodCovenantJSON+"\n```", _testInput); err != nil {
		t.Errorf("a fenced answer should still parse: %v", err)
	}
}

func TestParseCovenantRejects(t *testing.T) {
	tests := []struct {
		name, giveOld, giveNew, want string
	}{
		{
			name:    "undefined identifier",
			giveOld: `"expression": "capex / (opex + rent)"`,
			giveNew: `"expression": "capex / (opex + ebitda)"`,
			want:    `uses "ebitda"`,
		},
		{
			name:    "unused term",
			giveOld: `"expression": "capex / (opex + rent)"`,
			giveNew: `"expression": "capex / opex"`,
			want:    "never used",
		},
		{
			name:    "empty expression",
			giveOld: `"expression": "capex / (opex + rent)"`,
			giveNew: `"expression": ""`,
			want:    "expression is empty",
		},
		{
			name:    "operator not comparable",
			giveOld: `"op": "<="`,
			giveNew: `"op": "="`,
			want:    "not one of",
		},
		{
			name:    "threshold missing",
			giveOld: `"threshold": "0.42"`,
			giveNew: `"threshold": ""`,
			want:    "threshold",
		},
		{
			name:    "unknown term kind",
			giveOld: `"kind": "statement_line", "line": "Капитальные затраты"`,
			giveNew: `"kind": "vibes", "line": "Капитальные затраты"`,
			want:    "not allowed",
		},
		{
			name:    "period runs backwards",
			giveOld: `"from": "2025-01-01", "to": "2025-12-31"`,
			giveNew: `"from": "2025-12-31", "to": "2025-01-01"`,
			want:    "ends",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(_goodCovenantJSON, tt.giveOld, tt.giveNew, 1)
			if body == _goodCovenantJSON {
				t.Fatalf("test setup did not modify the json (%q not found)", tt.giveOld)
			}
			_, err := parseCovenant(body, _testInput)
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParseDecimalTolerance(t *testing.T) {
	tests := []struct{ give, want string }{
		{"1500000.00", "1500000"},
		{"$1,500,000.00", "1500000"},
		{"0.42x", "0.42"},
		{"0.42X", "0.42"},
		{" 3500000 ", "3500000"},
	}
	for _, tt := range tests {
		got, err := parseDecimal(tt.give)
		if err != nil {
			t.Errorf("parseDecimal(%q): %v", tt.give, err)
			continue
		}
		if !got.Equal(mustDec(tt.want)) {
			t.Errorf("parseDecimal(%q) = %s, want %s", tt.give, got, tt.want)
		}
	}
	for _, bad := range []string{"", "не менее", "abc"} {
		if _, err := parseDecimal(bad); err == nil {
			t.Errorf("parseDecimal(%q) should fail", bad)
		}
	}
}

func TestExpressionFunctionsAreNotTerms(t *testing.T) {
	body := strings.Replace(_goodCovenantJSON,
		`"expression": "capex / (opex + rent)"`,
		`"expression": "max(capex, opex) - rent"`, 1)
	if _, err := parseCovenant(body, _testInput); err != nil {
		t.Errorf("max() should be allowed in an expression: %v", err)
	}
}

func TestTriggerTermsMustBeDeclared(t *testing.T) {
	body := strings.Replace(_goodCovenantJSON,
		`"trigger": null`,
		`"trigger": {"expression": "financing > 4000000", "description": "springing test", "source_quote": "q"}`, 1)
	if _, err := parseCovenant(body, _testInput); err == nil ||
		!strings.Contains(err.Error(), "financing") {
		t.Errorf("an undeclared trigger term must be rejected, got: %v", err)
	}

	withTerm := strings.Replace(body,
		`{"name": "rent",  "kind": "statement_line", "line": "Арендные платежи", "reclassification": "both"}`,
		`{"name": "rent",  "kind": "statement_line", "line": "Арендные платежи", "reclassification": "both"},
     {"name": "financing", "kind": "statement_line", "line": "Поступления по финансированию"}`, 1)
	spec, err := parseCovenant(withTerm, _testInput)
	if err != nil {
		t.Fatalf("a declared trigger term should parse: %v", err)
	}
	if spec.Trigger == nil || spec.Trigger.Expression != "financing > 4000000" {
		t.Errorf("trigger = %+v", spec.Trigger)
	}
}

func TestExpressionAcceptsNonASCIIIdentifiers(t *testing.T) {
	body := `{
      "clause_id": "6.1",
      "title": "Покрытие",
      "expression": "(выручка + поступления) / (опекс + капекс)",
      "terms": [
        {"name": "выручка",     "kind": "statement_line", "line": "Выручка"},
        {"name": "поступления", "kind": "statement_line", "line": "Поступления по финансированию"},
        {"name": "опекс",       "kind": "statement_line", "line": "Операционные затраты"},
        {"name": "капекс",      "kind": "statement_line", "line": "Капитальные затраты"}
      ],
      "op": ">=", "threshold": "1.20", "unit": "ratio",
      "period": {"kind": "fiscal_year", "from": "2025-01-01", "to": "2025-12-31"},
      "trigger": null, "carveouts": [], "evidence_kind": "ratio", "quote": "q", "confidence": 0.9
    }`
	spec, err := parseCovenant(body, _testInput)
	if err != nil {
		t.Fatalf("Cyrillic term names should parse: %v", err)
	}
	if len(spec.Terms) != 4 {
		t.Errorf("terms = %d, want 4", len(spec.Terms))
	}

	broken := strings.Replace(body, `"expression": "(выручка + поступления) / (опекс + капекс)"`,
		`"expression": "(выручка + поступления) / (опекс + прибыль)"`, 1)
	if _, err := parseCovenant(broken, _testInput); err == nil {
		t.Error("an undefined Cyrillic identifier must still be rejected")
	}
}

func TestParseCovenantRejectsUnknownEnums(t *testing.T) {
	tests := []struct{ giveOld, giveNew, want string }{
		{`"evidence_kind": "ratio"`, `"evidence_kind": "amount"`, "evidence_kind"},
		{`"unit": "ratio"`, `"unit": "x"`, "unit"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			body := strings.Replace(_goodCovenantJSON, tt.giveOld, tt.giveNew, 1)
			if body == _goodCovenantJSON {
				t.Fatalf("test setup did not modify the json (%q)", tt.giveOld)
			}
			_, err := parseCovenant(body, _testInput)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected a rejection mentioning %q, got %v", tt.want, err)
			}
		})
	}
}

func TestParseCovenantRequiresPeriodDates(t *testing.T) {
	tests := []struct{ name, give string }{
		{name: "no start", give: `"from": "", "to": "2025-12-31"`},
		{name: "no end", give: `"from": "2025-01-01", "to": ""`},
		{name: "neither", give: `"from": "", "to": ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(_goodCovenantJSON, `"from": "2025-01-01", "to": "2025-12-31"`, tt.give, 1)
			_, err := parseCovenant(body, _testInput)
			if err == nil || !strings.Contains(err.Error(), "period is incomplete") {
				t.Errorf("expected the period to be rejected, got %v", err)
			}
		})
	}
}

func TestNormaliseReclassification(t *testing.T) {
	for in, want := range map[string]string{
		"":                   domain.ReclassIgnore,
		"ignore":             domain.ReclassIgnore,
		"include_in":         domain.ReclassInclude,
		"include_all":        domain.ReclassInclude,
		"INCLUDED":           domain.ReclassInclude,
		"exclude_from":       domain.ReclassExclude,
		"both":               domain.ReclassBoth,
		"auditor_allocation": domain.ReclassBoth,
	} {
		got, err := normaliseReclassification(in)
		if err != nil {
			t.Errorf("normaliseReclassification(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normaliseReclassification(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := normaliseReclassification("maybe"); err == nil {
		t.Error("an unknown reclassification policy must be rejected")
	}
}

func TestNormaliseEntitySource(t *testing.T) {
	if got := normaliseEntitySource(domain.TermStatementLine, "Shymkent Refinery JSC"); got != "" {
		t.Errorf("entity_source on a statement line = %q, want it dropped", got)
	}
	if got := normaliseEntitySource(domain.TermRelatedPartyPayments, "KYC"); got != "kyc" {
		t.Errorf("kyc normalisation = %q", got)
	}
	if got := normaliseEntitySource(domain.TermRelatedPartyPayments, "МСФО 24"); got != "ias24" {
		t.Errorf("ias24 normalisation = %q", got)
	}
	if got := normaliseEntitySource(domain.TermRelatedPartyPayments, "vibes"); got != "" {
		t.Errorf("unknown source = %q, want empty", got)
	}
}

func TestParseCovenantJSONAcceptsAWellFormedCorrection(t *testing.T) {
	corrected := strings.Replace(_goodCovenantJSON, `"threshold": "0.42"`, `"threshold": "0.38"`, 1)

	spec, err := parseCovenantJSON(json.RawMessage(corrected), _testInput)
	if err != nil {
		t.Fatalf("parseCovenantJSON: %v", err)
	}
	if !spec.Threshold.Equal(mustDec("0.38")) {
		t.Errorf("threshold = %s, want the corrected 0.38", spec.Threshold)
	}
	if spec.ScenarioID != "P1" || spec.ClauseID != "6.1" {
		t.Errorf("the correction must stay bound to its cell, got %s/%s", spec.ScenarioID, spec.ClauseID)
	}
}

func TestParseCovenantJSONRejectsACorrectionThatFailsValidation(t *testing.T) {
	tests := []struct{ name, give string }{
		{
			name: "term that no expression uses",
			give: strings.Replace(_goodCovenantJSON,
				`"expression": "capex / (opex + rent)"`, `"expression": "capex / opex"`, 1),
		},
		{
			name: "expression over an undeclared term",
			give: strings.Replace(_goodCovenantJSON,
				`"expression": "capex / (opex + rent)"`, `"expression": "capex / (opex + leases)"`, 1),
		},
		{
			name: "unit outside the vocabulary",
			give: strings.Replace(_goodCovenantJSON, `"unit": "ratio"`, `"unit": "EUR"`, 1),
		},
		{
			name: "period without dates",
			give: strings.Replace(_goodCovenantJSON,
				`"period": {"kind": "fiscal_year", "from": "2025-01-01", "to": "2025-12-31"}`,
				`"period": {"kind": "fiscal_year", "from": "", "to": ""}`, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCovenantJSON(json.RawMessage(tt.give), _testInput); err == nil {
				t.Error("an invalid correction must be rejected so the previous spec survives")
			}
		})
	}
}

func TestParseCovenantJSONRefusesAnythingButAnObject(t *testing.T) {
	quoted, err := json.Marshal(_goodCovenantJSON)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		give json.RawMessage
	}{
		{name: "the spec as a quoted string", give: quoted},
		{name: "a fenced string", give: json.RawMessage(`"` + "```json {}```" + `"`)},
		{name: "null", give: json.RawMessage(`null`)},
		{name: "an array", give: json.RawMessage(`[]`)},
		{name: "truncated json", give: json.RawMessage(`{"clause_id": "6.1",`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCovenantJSON(tt.give, _testInput); err == nil {
				t.Error("want a rejection, so the caller keeps the previous specification")
			}
		})
	}
}

func mustDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
