package stability

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestDifferencesNamesWhatMoved(t *testing.T) {
	base := answer{status: "BREACH", actual: dec("1.68"), evidence: "TXN-B1-0020"}

	tests := []struct {
		name string
		got  answer
		want string
	}{
		{"identical", base, ""},
		{"status", answer{status: "COMPLIANT", actual: dec("1.68"), evidence: "TXN-B1-0020"}, "status"},
		{"actual", answer{status: "BREACH", actual: dec("1.92"), evidence: "TXN-B1-0020"}, "actual"},
		{"evidence", answer{status: "BREACH", actual: dec("1.68"), evidence: "—"}, "evidence"},
		{"all three", answer{status: "COMPLIANT", actual: dec("0.40"), evidence: "—"}, "status+actual+evidence"},
	}
	for _, tt := range tests {
		if got := joinWhat(differences(base, tt.got)); got != tt.want {
			t.Errorf("%s: differences = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestActualWithinTolerance(t *testing.T) {
	if !within(dec("6842117.53"), dec("6842117.54")) {
		t.Error("a one-cent difference on seven million is the same answer")
	}
	if !within(dec("1.68"), dec("1.6805")) {
		t.Error("a difference under 0.5% is the same answer")
	}
	if within(dec("1.68"), dec("1.75")) {
		t.Error("4% apart is a different answer")
	}
	if within(dec("0"), dec("1")) {
		t.Error("zero and one are never the same answer")
	}
	if !within(dec("0"), dec("0")) {
		t.Error("zero equals zero")
	}
}

func TestReportOKOnlyWhenEveryCellHeld(t *testing.T) {
	if !(&Report{Cells: 36, Stable: 36}).OK() {
		t.Error("36 of 36 stable must report OK")
	}
	if (&Report{Cells: 36, Stable: 35}).OK() {
		t.Error("one moved cell must not report OK")
	}
}

func TestOnlyScenariosNarrowsTheComparisonBase(t *testing.T) {
	base := map[string]answer{
		"P4/6.1":  {status: "COMPLIANT", actual: dec("0.33")},
		"P4/6.3":  {status: "BREACH", actual: dec("0.04")},
		"P9/6.1":  {status: "BREACH", actual: dec("0.22")},
		"P10/6.1": {status: "COMPLIANT", actual: dec("0.24")},
		"B1/6.2":  {status: "COMPLIANT", actual: dec("1284663.42")},
	}

	got := onlyScenarios(base, []string{"P4", "P9"})
	if len(got) != 3 {
		t.Fatalf("kept %d cells, want the 3 belonging to P4 and P9: %v", len(got), got)
	}
	for key := range got {
		if key != "P4/6.1" && key != "P4/6.3" && key != "P9/6.1" {
			t.Errorf("%s does not belong to the probed scenarios", key)
		}
	}
	// P10 не должен попасть из-за общего префикса с P1.
	if _, ok := onlyScenarios(base, []string{"P1"})["P10/6.1"]; ok {
		t.Error("P10 matched a probe of P1")
	}
}
