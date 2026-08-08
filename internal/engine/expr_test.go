package engine

import (
	"testing"

	"github.com/shopspring/decimal"
)

func vars(pairs map[string]string) map[string]decimal.Decimal {
	out := make(map[string]decimal.Decimal, len(pairs))
	for k, v := range pairs {
		out[k] = decimal.RequireFromString(v)
	}
	return out
}

func TestEvalExpr(t *testing.T) {
	v := vars(map[string]string{
		"revenue": "1000", "opex": "400", "rent": "100", "capex": "250",
		"payroll": "300", "utilities": "700",
	})
	tests := []struct{ expr, want string }{
		{"revenue", "1000"},
		{"revenue - opex", "600"},
		{"capex / (opex + rent)", "0.5"},
		{"(revenue - opex) / capex", "2.4"},
		{"max(payroll, utilities)", "700"},
		{"min(payroll, utilities)", "300"},
		{"revenue - max(payroll, utilities)", "300"},

		{"opex + rent * 2", "600"},
		{"-opex + revenue", "600"},
		{"revenue / 4 - 50", "200"},
	}
	for _, tt := range tests {
		got, err := EvalExpr(tt.expr, v)
		if err != nil {
			t.Errorf("%s: %v", tt.expr, err)
			continue
		}
		if !got.Equal(decimal.RequireFromString(tt.want)) {
			t.Errorf("%s = %s, want %s", tt.expr, got, tt.want)
		}
	}
}

func TestEvalExprReportsDivisionByZero(t *testing.T) {
	_, err := EvalExpr("revenue / opex", vars(map[string]string{"revenue": "10", "opex": "0"}))
	if err == nil || !IsDivByZero(err) {
		t.Fatalf("want a division-by-zero error, got %v", err)
	}
}

func TestEvalExprRejectsUnknownTerm(t *testing.T) {
	if _, err := EvalExpr("revenue - ebitda", vars(map[string]string{"revenue": "10"})); err == nil {
		t.Fatal("an unresolved term must be an error, not zero")
	}
}

func TestEvalExprRejectsMalformed(t *testing.T) {
	for _, expr := range []string{"revenue +", "(revenue", "revenue opex", "max(revenue)"} {
		if _, err := EvalExpr(expr, vars(map[string]string{"revenue": "1", "opex": "2"})); err == nil {
			t.Errorf("%q parsed without error", expr)
		}
	}
}

func TestExprIdentifiers(t *testing.T) {
	ids, err := ExprIdentifiers("(revenue - opex + one_off_items) / revenue")
	if err != nil {
		t.Fatalf("ExprIdentifiers: %v", err)
	}
	want := []string{"revenue", "opex", "one_off_items"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}

	ids, _ = ExprIdentifiers("revenue - max(payroll, taxes)")
	for _, id := range ids {
		if id == "max" {
			t.Fatal("max was returned as a term name")
		}
	}
}

func TestEvalCondition(t *testing.T) {
	v := vars(map[string]string{"financing_receipts": "5442118.93"})
	got, err := EvalCondition("financing_receipts > 4000000", v)
	if err != nil {
		t.Fatalf("EvalCondition: %v", err)
	}
	if !got {
		t.Error("the springing trigger should have fired")
	}
	got, _ = EvalCondition("financing_receipts <= 4000000", v)
	if got {
		t.Error("the same condition cannot hold in both directions")
	}
}

func TestEvalConditionRejectsNonComparison(t *testing.T) {
	if _, err := EvalCondition("financing_receipts", vars(map[string]string{"financing_receipts": "1"})); err == nil {
		t.Fatal("a condition without an operator must be an error")
	}
}
