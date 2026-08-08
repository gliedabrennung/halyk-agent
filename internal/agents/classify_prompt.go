package agents

import (
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func taxonomyBlock() string {
	desc := map[domain.Category]string{
		domain.CatRevenue:             "money earned from customers — every \"... sales settlement\" row",
		domain.CatFinancingReceipts:   "money drawn from a loan or facility; borrowed, not earned",
		domain.CatCapex:               "purchase of a capital asset (equipment, plant, vehicles)",
		domain.CatAssetTransfer:       "a capital asset moved to another group company rather than bought",
		domain.CatPayroll:             "wages, salaries, bonuses, severance, staff funding transfers",
		domain.CatUtilities:           "electricity, water, sewer, gas, heating, compressed air, metering",
		domain.CatRent:                "rent and lease payments for premises, land, yards, equipment sites",
		domain.CatTaxes:               "taxes, duties, levies, VAT, tax penalties and assessments",
		domain.CatInterestExpense:     "interest PAID or accrued on borrowings, including capitalised interest",
		domain.CatInterestIncome:      "interest EARNED on deposits, balances and securities",
		domain.CatInsurancePremiums:   "insurance premiums and bonds",
		domain.CatMarketing:           "advertising, media, sponsorship, exhibitions, marketing production",
		domain.CatProfessionalService: "advisory, consulting, management retainers, legal and arbitration",
		domain.CatTelecom:             "telecoms and connectivity",
		domain.CatOperatingCosts:      "servicing, repair, inspection and operating works on assets",
		domain.CatOtherOperating:      "an operating cost that fits none of the leaves above",
		domain.CatOtherIncome:         "a receipt that is not revenue, not financing and not a refund of a cost",
		domain.CatUnknown:             "you cannot tell; use this instead of guessing",
	}
	var b strings.Builder
	for _, c := range domain.Categories {
		fmt.Fprintf(&b, "  %-22s %s\n", c, desc[c])
	}
	return b.String()
}

func classifyInstruction() string {
	return `You classify transaction description patterns from a corporate ledger into a fixed taxonomy.

Each item is a PATTERN — the recurring wording shared by several ledger rows — plus sample rows.
You classify the pattern, and your answer applies to every row that carries it.

Categories:

` + taxonomyBlock() + `
Two things decide the answer, and neither is the counterparty name. Counterparties in this ledger
are near-unique and unrelated to what was bought: an electricity bill can be payable to a company
called "Northwind Catering". Read the description. Use the sign (outflow/inflow) to tell a cost
from its reversal.

"contra" is true when an INFLOW reverses an earlier cost of the SAME category — a refund, rebate,
credit note, returned deposit, recovered advance. Then category is the cost's category, not an
income category. It is false for costs and for genuine income.

The distinctions that matter here:

- "insurance claim reimbursement" / "insurance deductible recovery" are other_income: the insurer
  paying out a loss. They are NOT a refund of premiums. "insurance premium refund", "broker
  rebate" and "experience refund" ARE (insurance_premiums, contra true).
- "interest income on treasury bills" is interest_income. "interest rebate on early repayment" and
  "interest recovery on overpayment" reduce interest paid: interest_expense, contra true.
- "capitalised interest charge" is interest_expense, not capex.
- "term loan facility drawdown" is financing_receipts, never revenue.
- "payroll advance recovered from staff", "unclaimed payroll returned", "payroll accrual reversal"
  are payroll, contra true. "payroll accrual funding" and "payroll top-up transfer" are payroll
  costs, contra false.
- "sublet rent received" and "lease incentive received" reduce rent: rent, contra true.
- A "levy" can be either: "municipal tax levy" is taxes, "sewer discharge levy" is utilities.

Return STRICT JSON:

{"labels": [
  {"i": 0, "category": "one of the categories above", "contra": true|false,
   "confidence": 0.0, "rationale": "one short clause"}
]}

One entry per item, same "i" as the input. No prose, no markdown fence.`
}

func (it ClassifyItem) signs() string {
	s := fmt.Sprintf("%d outflow, %d inflow", it.Outflows, it.Inflows)
	if it.Unpriced > 0 {
		s += fmt.Sprintf(", %d with no amount in the export — sign unknown, do not read as a receipt", it.Unpriced)
	}
	return s
}

func classifyPrompt(items []ClassifyItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Classify these %d patterns.\n", len(items))
	for i, it := range items {
		fmt.Fprintf(&b, "\n[%d] %q  (%d rows: %s)\n", i, it.Pattern, it.Count, it.signs())
		for j, s := range it.Samples {
			cp := ""
			if j < len(it.Counterparties) {
				cp = "  ← " + it.Counterparties[j]
			}
			fmt.Fprintf(&b, "     %s%s\n", s, cp)
		}
	}
	return b.String()
}

const _resolveInstruction = `Two classifiers disagree about how to categorise a ledger description pattern.
One is a keyword rule, the other a language model. Decide which is right, or give a third answer.

Judge from the description wording and the inflow/outflow split alone. The counterparty name in
this ledger says nothing about what was bought.

Return STRICT JSON with the same shape and indices as the input:

{"labels": [{"i": 0, "category": "...", "contra": true|false, "confidence": 0.0, "rationale": "..."}]}

No prose, no markdown fence.`

func resolvePrompt(disputes []Dispute) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Taxonomy:\n\n%s\n", taxonomyBlock())
	fmt.Fprintf(&b, "Resolve these %d disagreements.\n", len(disputes))
	for i, d := range disputes {
		it := d.Item
		fmt.Fprintf(&b, "\n[%d] %q  (%d rows: %s)\n", i, it.Pattern, it.Count, it.signs())
		for _, s := range it.Samples {
			fmt.Fprintf(&b, "     %s\n", s)
		}
		fmt.Fprintf(&b, "     rule says:  %s (contra=%v)\n", d.RuleCat, d.RuleCtr)
		fmt.Fprintf(&b, "     model says: %s (contra=%v)\n", d.ModelCat, d.ModelCtr)
		if d.Reason != "" {
			fmt.Fprintf(&b, "     conflict:   %s\n", d.Reason)
		}
	}
	return b.String()
}
