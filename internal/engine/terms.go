package engine

import (
	"fmt"
	"slices"
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

	// Provisional: терм посчитан по признаку, восстановленному кодом, а не объявленному
	// спецификацией. Считается, но уверенность ячейки ограничивается.
	Provisional bool
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

	// Ветку выбирает объявленный kind терма, а не формулировка пункта. Единственное
	// исключение — EBITDA: это определение («выручка за вычетом операционных расходов»),
	// а не вид источника, и модель отдаёт его как обычную строку отчётности.
	switch {
	case term.Kind == domain.TermRelatedPartyPayments:
		return relatedPartyTerm(term, rows)

	case term.Kind == domain.TermGroupConsolidated:
		return groupTerm(term, in, rows), nil

	case mentionsEBITDA(line):
		return ebitdaTerm(term, in, rows)

	case term.Kind == domain.TermStatementNote:
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

	if term.EntityScope != "" {
		return scopedEntityTerm(term, in, rows, cat)
	}

	out := categoryTerm(term, rows, cat, cat == domain.CatCapex)
	applyReclassification(&out, term, in, cat)
	return out, nil
}

// scopedEntityTerm суммирует строки категории только по контрагентам, чей статус в
// факт-базе совпадает с объявленным в спеке (restricted / unrestricted). Статус — поле
// спецификации: движок не выводит его из формулировки пункта.
func scopedEntityTerm(term domain.Term, in *Inputs, rows []row, cat domain.Category) (termResult, error) {
	res := termResult{Name: term.Name, Provisional: term.ScopeInferred}

	inScope := make(map[string]bool)
	if in.Facts != nil {
		for _, p := range in.Facts.Parties {
			if p.Status != term.EntityScope {
				continue
			}
			if key := domain.EntityKey(p.Name); key != "" {
				inScope[key] = true
			}
		}
	}
	if len(inScope) == 0 {

		res.Unmeasurable = true
		res.Trace = fmt.Sprintf(
			"%s: the dossier names no %s counterparty, so %s rows cannot be narrowed to them",
			term.Name, term.EntityScope, cat)
	}

	sum := decimal.Zero
	for _, r := range rows {
		if r.label.Category != cat {
			continue
		}
		if len(inScope) > 0 && !inScope[domain.EntityKey(r.txn.Counterparty)] {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()
	if res.Trace == "" {
		res.Trace = fmt.Sprintf("%s = %s over %d %s row(s) booked to a %s counterparty",
			term.Name, res.Value.StringFixed(2), len(res.Contributors), cat, term.EntityScope)
	}
	if term.ScopeInferred {
		res.Trace += fmt.Sprintf("  [scope %q was read off the clause wording, not declared by the "+
			"specification; re-run `covenants` so the model states entity_scope]", term.EntityScope)
	}
	return res, nil
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

// mentionsEBITDA распознаёт EBITDA и её раскрытые определения. Это словарь синонимов
// одного показателя, а не подгонка под конкретный договор: терм с такой строкой считается
// как выручка минус операционные расходы, откуда бы формулировка ни пришла.
func mentionsEBITDA(line string) bool {
	if containsAny(line, "ebitda", "прибыль до вычета", "earnings before interest") {
		return true
	}
	return containsAny(line, "выручк", "revenue") &&
		containsAny(line, "операционн", "operating") &&
		containsAny(line, "за вычетом", "минус", "less", "net of")
}

func mentionsOneOff(line string) bool {
	return containsAny(line, "разов", "one-off", "one off", "add-back", "обратному добавлению")
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
	res.Contributors = slices.Concat(revenue.Contributors, opex.Contributors)
	res.Trace = fmt.Sprintf("%s = revenue %s - operating costs %s = %s",
		term.Name, revenue.Value.StringFixed(2), opex.Value.StringFixed(2), res.Value.StringFixed(2))
	return res, nil
}

func noteTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}
	if in.Facts == nil {
		return res, nil
	}
	oneOff := mentionsOneOff(strings.ToLower(term.Line + " " + term.Description))

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
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
