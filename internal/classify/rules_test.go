package classify

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestRulesOnTheCorpusTraps(t *testing.T) {
	tests := []struct {
		pattern string
		cat     domain.Category
		contra  bool
	}{

		{"insurance claim reimbursement", domain.CatOtherIncome, false},
		{"insurance deductible recovery", domain.CatOtherIncome, false},
		{"insurance premium refund", domain.CatInsurancePremiums, true},
		{"insurance broker rebate", domain.CatInsurancePremiums, true},
		{"group insurance experience refund", domain.CatInsurancePremiums, true},
		{"unearned insurance premium return", domain.CatInsurancePremiums, true},
		{"business interruption insurance", domain.CatInsurancePremiums, false},

		{"interest income on treasury bills", domain.CatInterestIncome, false},
		{"interest credited on current account", domain.CatInterestIncome, false},
		{"interest rebate on early repayment", domain.CatInterestExpense, true},
		{"interest recovery on overpayment", domain.CatInterestExpense, true},
		{"capitalised interest charge", domain.CatInterestExpense, false},
		{"interest on finance sublease", domain.CatInterestExpense, false},

		{"term loan facility drawdown for cold store expansion", domain.CatFinancingReceipts, false},
		{"refinery product sales settlement", domain.CatRevenue, false},
		{"purchase of quayside crane equipment", domain.CatCapex, false},
		{"transfer of conveyor equipment to subsidiary", domain.CatAssetTransfer, false},

		{"payroll advance recovered from staff", domain.CatPayroll, true},
		{"payroll accrual reversal", domain.CatPayroll, true},
		{"payroll accrual funding", domain.CatPayroll, false},
		{"payroll top-up transfer", domain.CatPayroll, false},

		{"municipal tax levy", domain.CatTaxes, false},
		{"sewer discharge levy", domain.CatUtilities, false},
		{"excise tax credit received", domain.CatTaxes, true},
		{"tax overpayment refunded", domain.CatTaxes, true},
		{"electricity overbilling refund", domain.CatUtilities, true},
		{"compressed air utility charge", domain.CatUtilities, false},

		{"lease incentive received", domain.CatRent, true},
		{"rent overpayment refunded", domain.CatRent, true},
		{"antenna mast lease", domain.CatRent, false},
		{"office rent", domain.CatRent, false},

		{"marketing volume rebate", domain.CatMarketing, true},
		{"unused ad campaign budget returned", domain.CatMarketing, true},
		{"industry exhibition stand marketing", domain.CatMarketing, false},
		{"radio ad campaign schedule", domain.CatMarketing, false},

		{"telecom service credit received", domain.CatTelecom, true},
		{"broadband telecom invoice", domain.CatTelecom, false},
		{"management advisory retainer", domain.CatProfessionalService, false},
		{"demurrage dispute arbitration and legal servicing", domain.CatProfessionalService, false},
		{"berth silt cleaning and clearance works", domain.CatOperatingCosts, false},
		{"plant operating and maintenance expenses", domain.CatOperatingCosts, false},
	}
	for _, tt := range tests {
		r, ok := Classify(tt.pattern)
		if !ok {
			t.Errorf("no rule matched %q", tt.pattern)
			continue
		}
		if r.Cat != tt.cat || r.Contra != tt.contra {
			t.Errorf("%q: rule %s gave %s (contra=%v), want %s (contra=%v)",
				tt.pattern, r.ID, r.Cat, r.Contra, tt.cat, tt.contra)
		}
	}
}

func TestRuleCategoriesAreInTheTaxonomy(t *testing.T) {
	for _, r := range _rules {
		if !domain.ValidCategory(r.Cat) {
			t.Errorf("rule %s produces %q, which is not a category", r.ID, r.Cat)
		}
		if r.Cat == domain.CatUnknown {
			t.Errorf("rule %s produces unknown; a rule that cannot decide must not fire", r.ID)
		}
	}
}

func TestClassifyReportsNoMatch(t *testing.T) {
	if _, ok := Classify("wholly unrelated wording"); ok {
		t.Fatal("a pattern matching no rule must report false, not a default category")
	}
}

// Правила contra построены как «словарь категории + словарь возврата», поэтому обязаны
// срабатывать на формулировках, которых в этом корпусе нет. Если такой тест падает — правило
// снова выродилось в список заученных описаний.
func TestContraRulesGeneraliseBeyondTheCorpusWording(t *testing.T) {
	tests := []struct {
		pattern string
		cat     domain.Category
		contra  bool
	}{
		{"insurance premium write-back after audit", domain.CatInsurancePremiums, true},
		{"interest overcharge refunded by the lender", domain.CatInterestExpense, true},
		{"wages overpayment recovered from a leaver", domain.CatPayroll, true},
		{"district heating overbilling reversed", domain.CatUtilities, true},
		{"customs duty reclaim received", domain.CatTaxes, true},
		{"ground lease deposit released on exit", domain.CatRent, true},
		{"sponsorship contribution refunded after cancellation", domain.CatMarketing, true},
		{"broadband outage credit note", domain.CatTelecom, true},

		// А это по-прежнему расходы и доходы, не возвраты.
		{"interest on export credit line", domain.CatInterestExpense, false},
		{"credit facility interest", domain.CatInterestExpense, false},
		{"interest earned on escrow balance", domain.CatInterestIncome, false},
		{"quarterly insurance premium instalment", domain.CatInsurancePremiums, false},
		{"water supply charge for the depot", domain.CatUtilities, false},
	}
	for _, c := range tests {
		r, ok := Classify(c.pattern)
		if !ok {
			t.Errorf("%q matched no rule", c.pattern)
			continue
		}
		if r.Cat != c.cat || r.Contra != c.contra {
			t.Errorf("%q -> %s (contra=%v) by %s; want %s (contra=%v)",
				c.pattern, r.Cat, r.Contra, r.ID, c.cat, c.contra)
		}
	}
}
