package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

type Inputs struct {
	ScenarioID string
	Facts      *domain.FactBase
	Labels     *domain.LabelSet

	Txns []domain.Txn
}

type row struct {
	txn   domain.Txn
	label domain.TxnLabel
}

func (in *Inputs) rows() []row {
	labels := make(map[string]domain.TxnLabel)
	if in.Labels != nil {
		for _, l := range in.Labels.Txns {
			labels[l.TxnID] = l
		}
	}
	excluded := make(map[string]bool)
	fixes := make(map[string]decimal.Decimal)
	if in.Facts != nil {
		for _, a := range in.Facts.Adjustments {
			if !a.Applied || a.TxnID == "" {
				continue
			}
			switch a.Kind {
			case domain.AdjExcludePeriod:
				excluded[a.TxnID] = true
			case domain.AdjLedgerAmountFix:
				fixes[a.TxnID] = a.Amount.Abs()
			}
		}
	}

	out := make([]row, 0, len(in.Txns))
	for _, t := range in.Txns {
		if excluded[t.ID] {
			continue
		}
		l := labels[t.ID]
		if amount, ok := fixes[t.ID]; ok {

			if isInflowCategory(l.Category) && !l.Contra {
				t.AmountUSD = amount
			} else {
				t.AmountUSD = amount.Neg()
			}
			t.AmountMissing = false
		}
		out = append(out, row{txn: t, label: l})
	}
	return out
}

func isInflowCategory(c domain.Category) bool {
	switch c {
	case domain.CatRevenue, domain.CatFinancingReceipts, domain.CatInterestIncome, domain.CatOtherIncome:
		return true
	}
	return false
}

type termResult struct {
	Name         string
	Value        decimal.Decimal
	Contributors []string
	Trace        string

	Unmeasurable bool
}

func computeTerms(
	spec *domain.CovenantSpec,
	in *Inputs,
) (map[string]decimal.Decimal, []termResult, error) {
	rows := in.rows()
	vars := make(map[string]decimal.Decimal, len(spec.Terms))
	var results []termResult

	for _, term := range spec.Terms {
		res, err := computeTerm(spec, term, in, rows)
		if err != nil {
			return nil, nil, err
		}
		vars[strings.ToLower(res.Name)] = res.Value
		results = append(results, res)
	}
	return vars, results, nil
}

func computeTerm(
	spec *domain.CovenantSpec,
	term domain.Term,
	in *Inputs,
	rows []row,
) (termResult, error) {
	res := termResult{Name: term.Name}

	if term.Kind == domain.TermConstant {
		res.Value = term.Constant.Abs()
		res.Trace = fmt.Sprintf("%s = %s (literal in the clause)", term.Name, res.Value)
		return res, nil
	}

	line := strings.ToLower(term.Line)

	switch {
	case term.Kind == domain.TermRelatedPartyPayments || mentionsRelatedParty(line):
		return relatedPartyTerm(term, rows)

	case term.Kind == domain.TermGroupConsolidated:
		return groupTerm(term, in, rows), nil

	case mentionsEBITDA(line):
		return ebitdaTerm(term, in, rows)

	case term.Kind == domain.TermStatementNote || mentionsOneOff(line):
		return noteTerm(term, in, rows)
	}

	cat, ok := domain.CategoryForLine(term.Line)
	if !ok {
		cat, ok = domain.CategoryForLine(term.Description)
	}
	if !ok {
		return res, fmt.Errorf("%s/%s: term %q names line %q, which maps to no category",
			spec.ScenarioID, spec.ClauseID, term.Name, term.Line)
	}

	if cat == domain.CatAssetTransfer && strings.Contains(line, "неограниченн") {
		return unrestrictedTransferTerm(term, in, rows)
	}

	out := categoryTerm(term, rows, cat, cat == domain.CatCapex)
	applyReclassification(&out, term, in, cat)
	return out, nil
}

func unrestrictedTransferTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}

	unrestricted := make(map[string]bool)
	if in.Facts != nil {
		for _, p := range in.Facts.Parties {
			if p.Status == domain.StatusUnrestricted {
				if key := domain.EntityKey(p.Name); key != "" {
					unrestricted[key] = true
				}
			}
		}
	}
	if len(unrestricted) == 0 {

		res.Unmeasurable = true
		res.Trace = fmt.Sprintf(
			"%s: the dossier discloses no subsidiary security coverage, "+
				"so restricted and unrestricted transfers cannot be told apart", term.Name)
	}

	sum := decimal.Zero
	for _, r := range rows {
		if r.label.Category != domain.CatAssetTransfer {
			continue
		}
		if len(unrestricted) > 0 && !unrestricted[domain.EntityKey(r.txn.Counterparty)] {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()
	if res.Trace == "" {
		res.Trace = fmt.Sprintf("%s = %s over %d transfer(s) to unrestricted subsidiaries",
			term.Name, res.Value.StringFixed(2), len(res.Contributors))
	}
	return res, nil
}

func mentionsEBITDA(line string) bool {
	if strings.Contains(line, "ebitda") {
		return true
	}
	return strings.Contains(line, "выручк") && strings.Contains(line, "операционн") &&
		(strings.Contains(line, "за вычетом") || strings.Contains(line, "минус"))
}

func mentionsOneOff(line string) bool {
	for _, n := range []string{"разов", "one-off", "one off", "add-back", "обратному добавлению"} {
		if strings.Contains(line, n) {
			return true
		}
	}
	return false
}

func mentionsRelatedParty(line string) bool {
	if strings.Contains(line, "аффилирован") || strings.Contains(line, "ограниченные платежи") {
		return true
	}
	return strings.Contains(line, "связанн") && strings.Contains(line, "сторон")
}

func relatedPartyTerm(term domain.Term, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}
	sum := decimal.Zero
	for _, r := range rows {
		if !r.label.RelatedParty || !r.txn.AmountUSD.IsNegative() {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()
	res.Trace = fmt.Sprintf("%s = %s over %d payment(s) to related parties",
		term.Name, res.Value.StringFixed(2), len(res.Contributors))
	return res, nil
}

func ebitdaTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	revenue := categoryTerm(domain.Term{Name: "revenue"}, rows, domain.CatRevenue, false)
	opex := categoryTerm(domain.Term{Name: "opex"}, rows, domain.CatOperatingCosts, false)
	applyReclassification(&opex, domain.Term{Name: "opex"}, in, domain.CatOperatingCosts)

	res := termResult{Name: term.Name, Value: revenue.Value.Sub(opex.Value)}
	res.Contributors = append(append([]string{}, revenue.Contributors...), opex.Contributors...)
	res.Trace = fmt.Sprintf("%s = revenue %s - operating costs %s = %s",
		term.Name, revenue.Value.StringFixed(2), opex.Value.StringFixed(2), res.Value.StringFixed(2))
	return res, nil
}

func noteTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}
	if in.Facts == nil {
		return res, nil
	}
	line := strings.ToLower(term.Line + " " + term.Description)
	oneOff := strings.Contains(line, "разов") ||
		strings.Contains(line, "one-off") ||
		strings.Contains(line, "add-back")

	sum := decimal.Zero
	var parts []string
	for _, a := range in.Facts.Adjustments {
		if !a.Applied {
			continue
		}
		switch {
		case oneOff && a.Kind == domain.AdjEBITDAAddBack:

			if !deductedInOperatingCosts(a, rows) {
				continue
			}
		case !oneOff && a.Kind == domain.AdjDisclosedAmount:
		default:
			continue
		}
		sum = sum.Add(a.Amount.Abs())
		parts = append(parts, fmt.Sprintf("%s %s", a.Kind, a.Amount.Abs().StringFixed(2)))
	}
	res.Value = sum
	res.Trace = fmt.Sprintf("%s = %s from the notes (%s)",
		term.Name, res.Value.StringFixed(2), strings.Join(parts, ", "))
	return res, nil
}

func deductedInOperatingCosts(a domain.Adjustment, rows []row) bool {
	for _, r := range rows {
		if r.label.Category != domain.CatOperatingCosts {
			continue
		}
		if a.TxnID != "" && a.TxnID == r.txn.ID {
			return true
		}
		if !a.Amount.IsZero() && a.Amount.Abs().Equal(r.txn.AmountUSD.Abs()) {
			return true
		}
	}
	return false
}

func categoryTerm(
	term domain.Term,
	rows []row,
	cat domain.Category,
	includeTransfers bool,
) termResult {
	res := termResult{Name: term.Name}
	policy := reclassPolicy(term)
	sum := decimal.Zero
	for _, r := range rows {
		if !directionAllows(term.Direction, r) {
			continue
		}
		if !rowCountsIn(r, cat, policy, includeTransfers) {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()
	res.Trace = fmt.Sprintf("%s = %s over %d %s row(s)",
		term.Name, res.Value.StringFixed(2), len(res.Contributors), cat)
	return res
}

func rowCountsIn(r row, cat domain.Category, policy string, includeTransfers bool) bool {
	moved := r.label.Reclassified &&
		r.label.ReclassifiedTo != "" &&
		r.label.ReclassifiedTo != r.label.Category
	takesIn := policy == domain.ReclassInclude || policy == domain.ReclassBoth
	takesOut := policy == domain.ReclassExclude || policy == domain.ReclassBoth

	if moved && takesIn && r.label.ReclassifiedTo == cat {
		return true
	}
	if r.label.Category == cat {
		return !(moved && takesOut)
	}

	return includeTransfers && r.label.Category == domain.CatAssetTransfer
}

func groupTerm(term domain.Term, in *Inputs, rows []row) termResult {
	if in.Facts != nil {
		if capex, ok := in.Facts.GroupPPE.Capex(); ok {
			ppe := in.Facts.GroupPPE
			parent := ppe.Parent
			if parent == "" {
				parent = "the parent"
			}
			return termResult{
				Name:  term.Name,
				Value: capex,
				Trace: fmt.Sprintf(
					"%s = %s from %s consolidated statements: %s closing - %s opening + %s depreciation%s",
					term.Name, capex.StringFixed(2), parent,
					ppe.Closing.StringFixed(2), ppe.Opening.StringFixed(2), ppe.Depreciation.StringFixed(2),
					disposalNote(ppe.Disposals)),
			}
		}
	}

	out := categoryTerm(term, rows, domain.CatCapex, true)
	out.Unmeasurable = true
	out.Trace += "  [group figure: no usable consolidated statements for this borrower; " +
		"borrower-only capex used]"
	return out
}

func disposalNote(disposals decimal.Decimal) string {
	if disposals.IsZero() {
		return " (no disposals in the year)"
	}
	return fmt.Sprintf(" + %s disposals", disposals.Abs().StringFixed(2))
}

func reclassPolicy(term domain.Term) string {
	if term.Reclassification == "" || term.Reclassification == domain.ReclassIgnore {
		return domain.ReclassBoth
	}
	return term.Reclassification
}

func directionAllows(direction string, r row) bool {
	switch direction {
	case "outflow":
		return r.txn.AmountUSD.IsNegative() || r.label.Contra
	case "inflow":
		return !r.txn.AmountUSD.IsNegative() && !r.label.Contra
	}
	return true
}

func applyReclassification(res *termResult, term domain.Term, in *Inputs, cat domain.Category) {
	if in.Facts == nil {
		return
	}
	policy := reclassPolicy(term)
	for _, a := range in.Facts.Adjustments {
		if a.Kind != domain.AdjReclassify || !a.Applied || a.Amount.IsZero() {
			continue
		}

		if boundToRow(a, in) {
			continue
		}
		to, toOK := domain.CategoryForLine(a.ToCategory)
		from, fromOK := domain.CategoryForLine(a.FromCategory)
		if toOK && to == cat && (policy == domain.ReclassInclude || policy == domain.ReclassBoth) {
			res.Value = res.Value.Add(a.Amount.Abs())
			res.Trace += fmt.Sprintf("; +%s reclassified in by the auditor", a.Amount.Abs().StringFixed(2))
		}
		if fromOK && from == cat && (policy == domain.ReclassExclude || policy == domain.ReclassBoth) {
			res.Value = res.Value.Sub(a.Amount.Abs())
			res.Trace += fmt.Sprintf("; -%s reclassified out by the auditor", a.Amount.Abs().StringFixed(2))
		}
	}
}

func boundToRow(a domain.Adjustment, in *Inputs) bool {
	if in.Labels == nil {
		return false
	}
	for _, l := range in.Labels.Txns {
		if !l.Reclassified {
			continue
		}
		if a.TxnID != "" && a.TxnID == l.TxnID {
			return true
		}
		if a.Counterparty != "" && domain.EntityKey(a.Counterparty) == domain.EntityKey(l.Counterparty) {
			return true
		}
	}
	return false
}

func inPeriod(p domain.Period, t domain.Txn) bool {
	if !p.From.IsZero() && t.Date.Before(p.From) {
		return false
	}
	if !p.To.IsZero() && t.Date.After(p.To) {
		return false
	}
	return true
}

func scopeToPeriod(in *Inputs, p domain.Period) *Inputs {
	out := *in
	out.Txns = nil
	for _, t := range in.Txns {
		if inPeriod(p, t) {
			out.Txns = append(out.Txns, t)
		}
	}
	return &out
}

func sortedIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
