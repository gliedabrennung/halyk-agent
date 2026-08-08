package classify

import (
	"regexp"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

type Rule struct {
	ID  string
	Re  *regexp.Regexp
	Cat domain.Category

	Contra bool
}

func rule(id, expr string, cat domain.Category, contra bool) Rule {
	return Rule{ID: id, Re: regexp.MustCompile(`(?i)` + expr), Cat: cat, Contra: contra}
}

var _rules = []Rule{
	rule("revenue.sales", `sales settlement`, domain.CatRevenue, false),
	rule("financing.drawdown", `(facility|loan)\s+drawdown|drawdown\s+(of|on)\b`, domain.CatFinancingReceipts, false),
	rule("capex.purchase", `\bpurchase of\b`, domain.CatCapex, false),
	rule("capex.transfer", `\btransfer of\b.*\bto (a )?subsidiar`, domain.CatAssetTransfer, false),

	rule("interest.contra", `interest (rebate|recovery)`, domain.CatInterestExpense, true),
	rule("interest.income", `interest (income|credited)`, domain.CatInterestIncome, false),
	rule("interest.expense", `\binterest\b`, domain.CatInterestExpense, false),

	rule("insurance.claim", `insurance (claim reimbursement|deductible recovery)`, domain.CatOtherIncome, false),
	rule("insurance.contra", `insurance (premium refund|broker rebate|experience refund)|unearned insurance premium return`, domain.CatInsurancePremiums, true),
	rule("insurance.premium", `insurance|fidelity bond|workers comp`, domain.CatInsurancePremiums, false),

	rule("payroll.contra", `payroll (advance recovered|overfunding returned|agency rebate|accrual reversal|clearing account sweep back)|unclaimed payroll returned`, domain.CatPayroll, true),
	rule("payroll.cost", `payroll`, domain.CatPayroll, false),

	rule("utilities.contra", `(electricity|water|utility) .*(refund|credit|rebate|returned)`, domain.CatUtilities, true),
	rule("utilities.cost", `electricity|water|sewer|natural gas|district heating|compressed air|utilit`, domain.CatUtilities, false),

	rule("taxes.contra", `tax (credit received|overpayment refunded|assessment reversal|rebate)|tax reclaim received|(vat|excise) (refund|credit) received`, domain.CatTaxes, true),
	rule("taxes.cost", `\btax\b|\bvat\b|customs duty|excise`, domain.CatTaxes, false),

	rule("rent.contra", `rent (overpayment refunded|deposit returned|free period credit)|lease (incentive received|deposit released)|sublet rent received`, domain.CatRent, true),
	rule("rent.cost", `\brent\b|\blease\b`, domain.CatRent, false),

	rule("marketing.contra", `marketing (overbilling refund|agency credit note|volume rebate|co-operative funding received)|media buy rate adjustment credit|ad campaign budget returned`, domain.CatMarketing, true),
	rule("marketing.cost", `marketing|ad campaign|media buy|advertis|sponsorship|exhibition stand|point-of-sale`, domain.CatMarketing, false),

	rule("telecom.contra", `telecom service credit received`, domain.CatTelecom, true),
	rule("telecom.cost", `telecom|broadband`, domain.CatTelecom, false),

	rule("professional.services", `advisory|retainer|legal|arbitration|consult`, domain.CatProfessionalService, false),
	rule("operating.works", `servicing|repair|maintenance|inspection|survey|operating costs|remediation|clearance works|cleaning`, domain.CatOperatingCosts, false),
}

func Classify(pattern string) (Rule, bool) {
	for _, r := range _rules {
		if r.Re.MatchString(pattern) {
			return r, true
		}
	}
	return Rule{}, false
}
