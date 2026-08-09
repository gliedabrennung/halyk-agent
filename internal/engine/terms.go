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

	Contested bool
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
	markIndistinguishable(spec, results)
	return vars, results, nil
}

// markIndistinguishable flags terms that the specification names apart but that
// the ledger derives identically. Two names over one derivation carry no more
// information than one, and an expression that subtracts them reports a
// difference the documents never stated.
func markIndistinguishable(spec *domain.CovenantSpec, results []termResult) {
	kinds := make(map[string]domain.TermKind, len(spec.Terms))
	for _, t := range spec.Terms {
		kinds[t.Name] = t.Kind
	}

	shared := make(map[string][]int, len(results))
	for i, res := range results {
		if kinds[res.Name] == domain.TermConstant || res.Unmeasurable {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(res.Trace, res.Name))
		shared[body] = append(shared[body], i)
	}

	for _, idx := range shared {
		if len(idx) < 2 {
			continue
		}
		names := make([]string, 0, len(idx))
		for _, i := range idx {
			names = append(names, results[i].Name)
		}
		for _, i := range idx {
			results[i].Unmeasurable = true
			results[i].Trace += fmt.Sprintf(
				"; %s come to the same figure by the same reading, so the clause's own wording is what tells them apart and the documents do not",
				strings.Join(names, " and "))
		}
	}
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
	case term.Kind == domain.TermRelatedPartyPayments:
		return relatedPartyTerm(term, in, rows)

	case term.Kind == domain.TermGroupConsolidated:
		return groupTerm(term, in, rows), nil

	case mentionsEBITDA(line):
		return ebitdaTerm(spec, term, in, rows)

	case term.Kind == domain.TermStatementNote:
		return noteTerm(term, in, rows)
	}

	cat, ok, contested := termCategory(term, rows)
	if !ok {
		return noteTerm(term, in, rows)
	}

	if term.EntityScope != "" {
		return scopedEntityTerm(term, in, rows, cat)
	}

	out := categoryTerm(term, rows, cat, cat == domain.CatCapex)
	applyReclassification(&out, term, in, cat)
	if contested != "" {
		out.Contested = true
		out.Trace += contested
	}

	return out, nil
}

func termCategory(term domain.Term, rows []row) (domain.Category, bool, string) {
	read, fromWording := domain.CategoryForLine(term.Line)
	if !fromWording {
		read, fromWording = domain.CategoryForLine(term.Description)
	}
	switch {
	case term.Category == "":
		return read, fromWording, ""
	case !fromWording, read == term.Category:
		return term.Category, true, ""
	}

	declared, derived := rowsIn(rows, term.Category), rowsIn(rows, read)
	switch {
	case declared > 0 && derived == 0:
		return term.Category, true, fmt.Sprintf(
			"; the wording of the line reads as %s, which no row carries, so the declared %s is used",
			read, term.Category)
	case derived > 0 && declared == 0:
		return read, true, fmt.Sprintf(
			"; the specification calls this %s, which no row carries, so the wording's %s is used",
			term.Category, read)
	}
	return term.Category, true, fmt.Sprintf(
		"; the specification calls this %s and the wording reads as %s, and rows are booked to both",
		term.Category, read)
}

func rowsIn(rows []row, cat domain.Category) int {
	n := 0
	for _, r := range rows {
		if r.label.Category == cat {
			n++
		}
	}
	return n
}

func scopedEntityTerm(term domain.Term, in *Inputs, rows []row, cat domain.Category) (termResult, error) {
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
		res := categoryTerm(term, rows, cat, cat == domain.CatCapex)
		applyReclassification(&res, term, in, cat)
		res.Unmeasurable = true
		res.Trace += fmt.Sprintf("; the dossier names no %s counterparty, so the term is not narrowed",
			term.EntityScope)
		return res, nil
	}

	scoped := make([]row, 0, len(rows))
	for _, r := range rows {
		if inScope[domain.EntityKey(r.txn.Counterparty)] {
			scoped = append(scoped, r)
		}
	}
	res := categoryTerm(term, scoped, cat, cat == domain.CatCapex)
	res.Trace += fmt.Sprintf("; narrowed to %s counterparties", term.EntityScope)
	return res, nil
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}

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

func relatedPartyTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}
	sum := decimal.Zero
	for _, r := range rows {
		if !r.label.RelatedParty {
			continue
		}
		if r.txn.AmountMissing {
			res.Unmeasurable = true
			continue
		}
		if !r.txn.AmountUSD.IsNegative() {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()

	if declared := declaredRelated(in); len(res.Contributors) == 0 && declared > 0 {
		res.Unmeasurable = true
		res.Trace = fmt.Sprintf(
			"%s: the dossier names %d related part(ies), but no ledger row is booked to any of them",
			term.Name, declared)
		return res, nil
	}

	res.Trace = fmt.Sprintf("%s = %s over %d payment(s) to related parties",
		term.Name, res.Value.StringFixed(2), len(res.Contributors))
	return res, nil
}

func declaredRelated(in *Inputs) int {
	if in == nil || in.Facts == nil {
		return 0
	}
	n := 0
	for _, p := range in.Facts.Parties {
		if p.Related {
			n++
		}
	}
	return n
}

func ebitdaTerm(spec *domain.CovenantSpec, term domain.Term, in *Inputs, rows []row) (termResult, error) {
	revenue := categoryTerm(domain.Term{Name: "revenue"}, rows, domain.CatRevenue, false)
	opex := categoryTerm(domain.Term{Name: "opex"}, rows, domain.CatOperatingCosts, false)
	applyReclassification(&opex, domain.Term{Name: "opex"}, in, domain.CatOperatingCosts)

	raw := revenue.Value.Sub(opex.Value)
	res := termResult{Name: term.Name, Value: raw}
	res.Unmeasurable = revenue.Unmeasurable || opex.Unmeasurable
	res.Contributors = slices.Concat(revenue.Contributors, opex.Contributors)
	res.Trace = fmt.Sprintf("%s = revenue %s - operating costs %s = %s",
		term.Name, revenue.Value.StringFixed(2), opex.Value.StringFixed(2), raw.StringFixed(2))

	if termClaimsAddBacks(spec, term.Name) {
		return res, nil
	}
	added, parts := addBacks(in, rows)
	if len(parts) == 0 {
		return res, nil
	}
	res.Value = raw.Add(added)
	res.Trace += fmt.Sprintf("; + %s added back by the auditor = %s",
		strings.Join(parts, ", "), res.Value.StringFixed(2))
	return res, nil
}

func addBacks(in *Inputs, rows []row) (decimal.Decimal, []string) {
	sum := decimal.Zero
	if in.Facts == nil {
		return sum, nil
	}
	var parts []string
	for _, a := range in.Facts.Adjustments {
		if !a.Applied || a.Kind != domain.AdjEBITDAAddBack || !deductedInOperatingCosts(a, rows) {
			continue
		}
		sum = sum.Add(a.Amount.Abs())
		parts = append(parts, fmt.Sprintf("%s %s", a.Kind, a.Amount.Abs().StringFixed(2)))
	}
	return sum, parts
}

func termClaimsAddBacks(spec *domain.CovenantSpec, self string) bool {
	if spec == nil {
		return false
	}
	for _, t := range spec.Terms {
		if t.Name == self || t.Kind != domain.TermStatementNote {
			continue
		}
		if mentionsOneOff(strings.ToLower(t.Line + " " + t.Description)) {
			return true
		}
	}
	return false
}

func noteTerm(term domain.Term, in *Inputs, rows []row) (termResult, error) {
	res := termResult{Name: term.Name}
	if in.Facts == nil {
		return res, nil
	}
	sum, parts := addBacks(in, rows)
	if !mentionsOneOff(strings.ToLower(term.Line + " " + term.Description)) {
		sum, parts = decimal.Zero, nil
		for _, a := range in.Facts.Adjustments {
			if !a.Applied || a.Kind != domain.AdjDisclosedAmount {
				continue
			}
			sum = sum.Add(a.Amount.Abs())
			parts = append(parts, fmt.Sprintf("%s %s", a.Kind, a.Amount.Abs().StringFixed(2)))
		}
	}
	res.Value = sum
	if len(parts) == 0 {
		res.Unmeasurable = true
		res.Trace = fmt.Sprintf("%s: no ledger category carries %q and the notes disclose no figure for it",
			term.Name, term.Line)
		return res, nil
	}
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
	var unpriced []string
	for _, r := range rows {
		if !rowCountsIn(r, cat, policy, includeTransfers) {
			continue
		}
		if r.txn.AmountMissing {
			unpriced = append(unpriced, r.txn.ID)
			continue
		}
		if !directionAllows(term.Direction, r) {
			continue
		}
		sum = sum.Add(r.txn.AmountUSD)
		res.Contributors = append(res.Contributors, r.txn.ID)
	}
	res.Value = sum.Abs()
	res.Trace = fmt.Sprintf("%s = %s over %d %s row(s)",
		term.Name, res.Value.StringFixed(2), len(res.Contributors), cat)
	if len(unpriced) > 0 {
		res.Unmeasurable = true
		res.Trace += fmt.Sprintf("; %s carries no amount in the export and none was disclosed, so it counts as zero",
			strings.Join(unpriced, ", "))
	}
	if len(res.Contributors) == 0 && len(unpriced) == 0 && len(rows) > 0 {
		res.Unmeasurable = true
		res.Trace += "; no row of this borrower carries that category, so the term may be reading a different bucket than the ledger"
	}
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
