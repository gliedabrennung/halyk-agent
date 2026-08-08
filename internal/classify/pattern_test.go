package classify

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestPatternStripsDetailAndPeriod(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Digital media buy — display — Kyzylorda station", "digital media buy"},
		{"Cold store lease 2025", "cold store lease"},
		{"Cold store lease — Aktau", "cold store lease"},
		{"Advance cargo sales settlement fourth quarter", "advance cargo sales settlement"},
		{"Rotational staff payroll settlement Q4 2025", "rotational staff payroll settlement"},
		{"Loan interest payment second quarter", "loan interest payment"},
		{"  Electricity   bill  ", "electricity bill"},
	}
	for _, tt := range tests {
		if got := Pattern(tt.in); got != tt.want {
			t.Errorf("Pattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPatternMergesYearVariants(t *testing.T) {
	if Pattern("Corporate income tax instalment 2025") != Pattern("Corporate income tax instalment") {
		t.Fatal("the 2025 variant must collapse onto the bare pattern")
	}
}

func TestEntityKeyIgnoresLegalFormAndBranch(t *testing.T) {
	tests := []struct{ a, b string }{
		{"Aktau Holdings LLP", "Aktau Holdings L.L.P."},
		{"Atyrau Holding Group LLP", "Atyrau Holding Group L.L.P."},
		{"Hartley Building Services Holdings (Turkistan point)", "Hartley Building Services Holdings"},
		{"Kazyna Capital LLP", "kazyna capital"},
	}
	for _, tt := range tests {
		if domain.EntityKey(tt.a) != domain.EntityKey(tt.b) {
			t.Errorf("domain.EntityKey(%q)=%q must equal domain.EntityKey(%q)=%q", tt.a, domain.EntityKey(tt.a), tt.b, domain.EntityKey(tt.b))
		}
	}
}

func TestEntityKeyKeepsDistinctCompaniesApart(t *testing.T) {
	tests := []struct{ a, b string }{
		{"Atyrau Holding Group LLP", "Atyrau Energy Supply JSC"},
		{"Atyrau Holding Group LLP", "Atyrau Pump Station Services LLP"},
		{"Aktau Holdings LLP", "Aktau Port Services JSC"},
	}
	for _, tt := range tests {
		if domain.EntityKey(tt.a) == domain.EntityKey(tt.b) {
			t.Errorf("EntityKey collapsed %q and %q onto %q", tt.a, tt.b, domain.EntityKey(tt.a))
		}
	}
}
