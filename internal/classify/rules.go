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

func (r Rule) fired() bool { return r.ID != "" }

func rule(id, expr string, cat domain.Category, contra bool) Rule {
	return Rule{ID: id, Re: regexp.MustCompile(`(?i)` + expr), Cat: cat, Contra: contra}
}

const (
	_reversal = `(refund|refunded|rebate|reversal|reversed|recovered|recovery|reclaim|` +
		`returned|\breturn\b|released|sweep back|write-back|writeback|` +
		`credit note|credit received|funding received)`

	_reversalLoose = `(` + _reversal + `|\bcredit\b)`
)

var _rules = []Rule{
	rule("revenue.sales", `\bsales\b.*\b(settlement|proceeds|receipts?)\b|revenue from`, domain.CatRevenue, false),
	rule("financing.drawdown", `(facility|loan)\s+drawdown|drawdown\s+(of|on)\b`, domain.CatFinancingReceipts, false),
	rule("capex.purchase", `\bpurchase of\b`, domain.CatCapex, false),
	rule("capex.transfer", `\btransfer of\b.*\bto (a )?subsidiar`, domain.CatAssetTransfer, false),

	rule("interest.contra", `\binterest\b.*`+_reversal, domain.CatInterestExpense, true),
	rule("interest.income", `interest (income|credited|earned)`, domain.CatInterestIncome, false),
	rule("interest.expense", `\binterest\b`, domain.CatInterestExpense, false),

	rule("insurance.claim", `insurance.*(claim|deductible).*(reimburs|recovery|payout|settle)`,
		domain.CatOtherIncome, false),
	rule("insurance.contra", `(insurance|premium|fidelity bond).*`+_reversalLoose,
		domain.CatInsurancePremiums, true),
	rule("insurance.premium", `insurance|fidelity bond|workers comp`, domain.CatInsurancePremiums, false),

	rule("payroll.contra", `(payroll|wages|salaries).*`+_reversalLoose, domain.CatPayroll, true),
	rule("payroll.cost", `payroll`, domain.CatPayroll, false),

	rule("utilities.contra", `(electricity|water|sewer|gas|heating|utilit).*`+_reversalLoose,
		domain.CatUtilities, true),
	rule("utilities.cost", `electricity|water|sewer|natural gas|district heating|compressed air|utilit`,
		domain.CatUtilities, false),

	rule("taxes.contra", `(\btax\b|\bvat\b|excise|customs duty).*`+_reversalLoose, domain.CatTaxes, true),
	rule("taxes.cost", `\btax\b|\bvat\b|customs duty|excise`, domain.CatTaxes, false),

	rule("rent.contra", `(\brent\b|\blease\b|sublet).*`+_reversalLoose+
		`|sublet rent received|lease incentive received`, domain.CatRent, true),
	rule("rent.cost", `\brent\b|\blease\b`, domain.CatRent, false),

	rule("marketing.contra", `(marketing|ad campaign|media buy|advertis|sponsorship).*`+_reversalLoose,
		domain.CatMarketing, true),
	rule("marketing.cost", `marketing|ad campaign|media buy|advertis|sponsorship|exhibition stand|point-of-sale`,
		domain.CatMarketing, false),

	rule("telecom.contra", `(telecom|broadband).*`+_reversalLoose, domain.CatTelecom, true),
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
