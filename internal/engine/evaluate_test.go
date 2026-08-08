package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(d int) time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d-1) }

func ledgerRow(id string, date int, counterparty, amount string) domain.Txn {
	a := dec(amount)
	return domain.Txn{
		ID: id, ScenarioID: "P1", AccountID: "ACC-1", Date: day(date),
		Counterparty: counterparty, Amount: a, AmountUSD: a, Currency: "USD",
	}
}

func label(id string, cat domain.Category) domain.TxnLabel {
	return domain.TxnLabel{TxnID: id, Category: cat, Source: "rule+llm"}
}

func year2025() domain.Period {
	return domain.Period{Kind: "fiscal_year", From: day(1), To: day(365)}
}

func spec(expr, op, threshold string, terms ...domain.Term) *domain.CovenantSpec {
	return &domain.CovenantSpec{
		ScenarioID: "P1", ClauseID: "6.1", Expression: expr, Terms: terms,
		Op: op, Threshold: dec(threshold), Unit: "ratio", Period: year2025(),
		EvidenceKind: "aggregate", Confidence: 1,
	}
}

func term(name, line string) domain.Term {
	return domain.Term{Name: name, Kind: domain.TermStatementLine, Line: line}
}

func TestEvaluateComparesAgainstTheThreshold(t *testing.T) {
	in := &Inputs{
		ScenarioID: "P1",
		Facts:      &domain.FactBase{ScenarioID: "P1"},
		Labels: &domain.LabelSet{ScenarioID: "P1", Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "1000000"),
			ledgerRow("TXN-P1-0002", 20, "Contractor", "-600000"),
		},
	}
	s := spec("revenue - opex", ">=", "500000", term("revenue", "Выручка"), term("opex", "Операционные расходы"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH (400000 < 500000)", v.Status)
	}
	if !v.Actual.Equal(dec("400000")) {
		t.Errorf("actual = %s, want 400000", v.Actual)
	}
}

func TestEvaluateReportsMagnitudes(t *testing.T) {
	in := &Inputs{
		ScenarioID: "P1",
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatCapex),
		}},
		Txns: []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Supplier", "-1842006.44")},
	}
	s := spec("capex", "<=", "2000000", term("capex", "Капитальные затраты"))
	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("1842006.44")) {
		t.Errorf("actual = %s, want the positive magnitude", v.Actual)
	}
	if v.Status != domain.StatusCompliant {
		t.Errorf("status = %s, want COMPLIANT", v.Status)
	}
}

func TestEvaluateHonoursThePeriod(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatRevenue),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 60, "Customer", "3000000"),
			ledgerRow("TXN-P1-0002", 300, "Customer", "4000000"),
		},
	}
	s := spec("revenue", ">=", "3500000", term("revenue", "Выручка"))
	s.Period = domain.Period{Kind: "quarter", From: day(274), To: day(365)}

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("4000000")) {
		t.Errorf("actual = %s, want only the Q4 row", v.Actual)
	}
}

func TestEvaluateSkipsAnUntriggeredCovenant(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatFinancingReceipts),
			label("TXN-P1-0002", domain.CatRevenue),
			label("TXN-P1-0003", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Bank", "1000000"),
			ledgerRow("TXN-P1-0002", 20, "Customer", "2000000"),
			ledgerRow("TXN-P1-0003", 30, "Contractor", "-1500000"),
		},
	}
	s := spec("financing_receipts / ebitda", "<=", "1.7",
		term("financing_receipts", "поступления по финансированию"),
		term("revenue", "Выручка"),
		term("ebitda", "EBITDA"))
	s.Trigger = &domain.Condition{Expression: "financing_receipts > 4000000"}

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.TriggerFired {
		t.Error("the trigger must not fire below its threshold")
	}
	if v.Status != domain.StatusCompliant {
		t.Errorf("status = %s, want COMPLIANT for a covenant that does not apply", v.Status)
	}

	if !v.Actual.Equal(dec("2")) {
		t.Errorf("actual = %s, want 2 — the metric is measured whether or not the covenant applies", v.Actual)
	}
}

func TestEvaluateCarveoutKeepsTheMeasuredValue(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatCapex)}},
		Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Supplier", "-2500000")},
	}
	s := spec("capex", "<=", "2000000", term("capex", "Капитальные затраты"))
	s.Carveouts = []domain.Carveout{{
		Condition:   domain.Condition{Expression: "capex <= 3000000"},
		Description: "overspend up to $3m is permitted",
	}}

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusCompliant {
		t.Errorf("status = %s, want COMPLIANT under the carve-out", v.Status)
	}
	if v.CarveoutApplied == "" {
		t.Error("the carve-out that excused the breach must be named")
	}
	if !v.Actual.Equal(dec("2500000")) {
		t.Errorf("actual = %s, want the measured 2500000", v.Actual)
	}
}

func TestEvaluateDropsRowsTheAuditorExcluded(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{
			{Kind: domain.AdjExcludePeriod, TxnID: "TXN-P1-0002", Applied: true,
				Rationale: "services delivered in 2026"},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatOperatingCosts),
			label("TXN-P1-0002", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Contractor", "-3104882.61"),
			ledgerRow("TXN-P1-0002", 350, "Surveyor", "-612884.19"),
		},
	}
	s := spec("opex", "<=", "4000000", term("opex", "Операционные расходы"))
	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("3104882.61")) {
		t.Errorf("actual = %s, want the excluded row left out", v.Actual)
	}
}

func TestEvaluateFollowsTheAuditorsReclassification(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{
			{Kind: domain.AdjReclassify, Counterparty: "Tien Shan Advisory Bureau",
				Amount: dec("1104663.28"), FromCategory: "Консультационные услуги",
				ToCategory: "Операционные расходы", Applied: true},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatOperatingCosts),
			{TxnID: "TXN-P1-0002", Category: domain.CatProfessionalService,
				Counterparty: "Tien Shan Advisory Bureau", Reclassified: true,
				ReclassifiedTo: domain.CatOperatingCosts, AdjustmentKind: domain.AdjReclassify},
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Contractor", "-5918004.37"),
			ledgerRow("TXN-P1-0002", 20, "Tien Shan Advisory Bureau", "-1104663.28"),
		},
	}
	s := spec("opex", "<=", "6000000", term("opex", "Операционные затраты"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !v.Actual.Equal(dec("7022667.65")) {
		t.Errorf("actual = %s, want 7022667.65", v.Actual)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", v.Status)
	}
}

func TestEvaluateNetsReversalsAgainstTheirLine(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRent),
			{TxnID: "TXN-P1-0002", Category: domain.CatRent, Contra: true},
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Landlord", "-1000000"),
			ledgerRow("TXN-P1-0002", 20, "Landlord", "250000"),
		},
	}
	s := spec("rent", "<=", "900000", term("rent", "Арендные платежи"))
	s.Terms[0].Direction = "outflow"

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("750000")) {
		t.Errorf("actual = %s, want 750000 net of the refund", v.Actual)
	}
}

func TestEvaluateFillsAMissingAmountWithTheRightSign(t *testing.T) {
	blank := ledgerRow("TXN-P1-0033", 300, "State Revenue Committee", "0")
	blank.AmountMissing = true
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{
			{Kind: domain.AdjLedgerAmountFix, TxnID: "TXN-P1-0033", Amount: dec("486204.19"), Applied: true},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0033", domain.CatTaxes)}},
		Txns:   []domain.Txn{blank},
	}
	s := spec("taxes", "<=", "400000", term("taxes", "Налоги"))
	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("486204.19")) {
		t.Errorf("actual = %s, want the recovered amount", v.Actual)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", v.Status)
	}
}

func TestEvaluateCountsOnlyUnrestrictedTransfers(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{
			UnrestrictedThreshold: dec("50"),
			Parties: []domain.Party{
				{Name: "Zhezkazgan Conveyor Assets LLP", PledgedShare: dec("87.6"), Status: domain.StatusRestricted},
				{Name: "Zhezkazgan Processing Holdings LLP", PledgedShare: dec("11.4"), Status: domain.StatusUnrestricted},
			},
		},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0017", domain.CatAssetTransfer),
			label("TXN-P1-0025", domain.CatAssetTransfer),
			label("TXN-P1-0035", domain.CatCapex),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0017", 300, "Zhezkazgan Conveyor Assets LLP", "-302118.64"),
			ledgerRow("TXN-P1-0025", 250, "Zhezkazgan Processing Holdings LLP", "-418204.37"),
			ledgerRow("TXN-P1-0035", 180, "Ural Haul Systems LLP", "-1204663.28"),
		},
	}
	s := spec("transferred / capex", "<=", "0.15",
		domain.Term{Name: "transferred", Kind: domain.TermLedgerCategory,
			Line:        "совокупная стоимость капитальных активов, переданных дочерним организациям",
			EntityScope: domain.StatusUnrestricted},
		term("capex", "совокупные капитальные затраты"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !v.Actual.Equal(dec("0.22")) {
		t.Errorf("actual = %s, want 0.22", v.Actual)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", v.Status)
	}
}

func TestTransfersAreNotNarrowedWithoutADeclaredScope(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Parties: []domain.Party{
			{Name: "Zhezkazgan Conveyor Assets LLP", Status: domain.StatusRestricted},
			{Name: "Zhezkazgan Processing Holdings LLP", Status: domain.StatusUnrestricted},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0017", domain.CatAssetTransfer),
			label("TXN-P1-0025", domain.CatAssetTransfer),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0017", 300, "Zhezkazgan Conveyor Assets LLP", "-100000"),
			ledgerRow("TXN-P1-0025", 250, "Zhezkazgan Processing Holdings LLP", "-400000"),
		},
	}
	s := spec("transferred", "<=", "1000000",
		domain.Term{Name: "transferred", Kind: domain.TermLedgerCategory,
			Line: "капитальные активы, переданные Неограниченным дочерним организациям"})

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("500000")) {
		t.Errorf("actual = %s, want 500000: every transfer counts when no scope is declared", v.Actual)
	}
}

func TestCarveoutCapDoesNotExcuseAnUnboundedBreach(t *testing.T) {
	capped := func(amount string) *domain.CovenantSpec {
		s := spec("capex", "<=", "2000000", term("capex", "Капитальные затраты"))
		s.Unit = domain.UnitUSD
		s.Carveouts = []domain.Carveout{{
			Condition:   domain.Condition{Expression: "capex >= 0"},
			Description: "overspend permitted with lender consent",
			Cap:         dec(amount),
		}}
		return s
	}
	rows := func(amount string) *Inputs {
		return &Inputs{
			Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatCapex)}},
			Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Supplier", amount)},
		}
	}

	within, err := Evaluate(capped("600000"), rows("-2500000"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if within.Status != domain.StatusCompliant {
		t.Errorf("status = %s, want COMPLIANT: the 500000 overage fits the 600000 cap", within.Status)
	}
	if within.CarveoutApplied == "" {
		t.Error("the carve-out that excused the breach must be named")
	}

	beyond, err := Evaluate(capped("400000"), rows("-2500000"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if beyond.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH: a 500000 overage runs past a 400000 cap", beyond.Status)
	}
	if beyond.CarveoutApplied != "" {
		t.Errorf("carve-out %q must not be recorded when its cap does not reach", beyond.CarveoutApplied)
	}
	if !beyond.Actual.Equal(dec("2500000")) {
		t.Errorf("actual = %s, want the measured 2500000 either way", beyond.Actual)
	}
}

func TestCarveoutCapOnARatioIsFlaggedRatherThanGuessed(t *testing.T) {
	s := spec("capex / revenue", "<=", "0.4",
		term("capex", "Капитальные затраты"), term("revenue", "Выручка"))
	s.Unit = domain.UnitRatio
	s.Confidence = 1
	s.Carveouts = []domain.Carveout{{
		Condition:   domain.Condition{Expression: "capex >= 0"},
		Description: "overspend permitted with lender consent",
		Cap:         dec("500000"),
	}}
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatCapex),
			label("TXN-P1-0002", domain.CatRevenue),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Supplier", "-600000"),
			ledgerRow("TXN-P1-0002", 20, "Customer", "1000000"),
		},
	}

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusCompliant {
		t.Errorf("status = %s, want COMPLIANT: the clause permits the excess and the cap is unreadable", v.Status)
	}
	if v.Confidence > 0.3 {
		t.Errorf("confidence = %v, want it lowered so the cell reaches review", v.Confidence)
	}
}

func TestUnreadableTriggerTestsTheCovenantAndSaysSo(t *testing.T) {
	s := spec("capex", "<=", "2000000", term("capex", "Капитальные затраты"))
	s.Confidence = 1
	s.Trigger = &domain.Condition{Expression: "capex"}
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatCapex)}},
		Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Supplier", "-2500000")},
	}

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH: a trigger that cannot be read must not excuse the covenant", v.Status)
	}
	if v.Confidence > 0.3 {
		t.Errorf("confidence = %v, want it lowered", v.Confidence)
	}
	for _, line := range v.Trace {
		if strings.Contains(line, "= true") {
			t.Errorf("the trace claims the trigger evaluated: %q", line)
		}
	}
}

func TestOneOffAddBackOnlyCountsWhatOperatingCostsDeducted(t *testing.T) {
	build := func(opexAmount string) *Inputs {
		return &Inputs{
			Facts: &domain.FactBase{Adjustments: []domain.Adjustment{{
				Kind:    domain.AdjEBITDAAddBack,
				Amount:  dec("400000"),
				Applied: true,
			}}},
			Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
				label("TXN-P1-0001", domain.CatRevenue),
				label("TXN-P1-0002", domain.CatOperatingCosts),
			}},
			Txns: []domain.Txn{
				ledgerRow("TXN-P1-0001", 10, "Customer", "3000000"),
				ledgerRow("TXN-P1-0002", 20, "Contractor", opexAmount),
			},
		}
	}
	s := spec("revenue - opex + one_off_items", ">=", "3000000",
		term("revenue", "Выручка"),
		term("opex", "Операционные расходы"),
		domain.Term{Name: "one_off_items", Kind: domain.TermStatementNote, Line: "разовые статьи"})

	deducted, err := Evaluate(s, build("-400000"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !deducted.Actual.Equal(dec("3000000")) {
		t.Errorf("actual = %s, want 3000000: the add-back restores what opex removed", deducted.Actual)
	}

	absent, err := Evaluate(s, build("-777000"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !absent.Actual.Equal(dec("2223000")) {
		t.Errorf("actual = %s, want 2223000: an add-back with nothing behind it must not be counted", absent.Actual)
	}
	if absent.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", absent.Status)
	}
}

func TestDisclosedAmountFeedsATermThatIsNotAnAddBack(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{
			{Kind: domain.AdjDisclosedAmount, Amount: dec("918447.52"), Applied: true},
			{Kind: domain.AdjDisclosedAmount, Amount: dec("100000"), Applied: false},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatPayroll)}},
		Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Staff", "-3302867.43")},
	}
	s := spec("payroll + severance", "<=", "4000000",
		term("payroll", "Расходы на оплату труда"),
		term("severance", "Обязательство по выходным пособиям"))
	s.Terms[1].Kind = domain.TermStatementNote

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("4221314.95")) {
		t.Errorf("actual = %s, want 4221314.95 — the rejected disclosure must not be added", v.Actual)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", v.Status)
	}
}

func TestRelatedPartyTermIsUnmeasurableWhenNoRowMatchesTheDossier(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Parties: []domain.Party{
			{Name: "Quarry Holding LLP", VotingShare: dec("46.8"), Related: true},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Someone Else LLP", "-4204663.19")},
	}
	s := spec("related_party_payments / opex", "<=", "0.08",
		domain.Term{Name: "related_party_payments", Kind: domain.TermRelatedPartyPayments,
			Line: "платежи в пользу связанных сторон"},
		term("opex", "Операционные расходы"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Confidence > 0.2 {
		t.Errorf("confidence = %v, want it dropped: the dossier names a related party and nothing matched", v.Confidence)
	}
	if !slices.ContainsFunc(v.Trace, func(l string) bool { return strings.Contains(l, "no ledger row is booked") }) {
		t.Errorf("the trace must say the dossier went unmatched: %q", v.Trace)
	}
}

func TestRelatedPartyTermStaysMeasurableWhenTheDossierNamesNobody(t *testing.T) {
	in := &Inputs{
		Facts:  &domain.FactBase{},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatOperatingCosts)}},
		Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Someone Else LLP", "-1000")},
	}
	s := spec("related_party_payments", "<=", "500000",
		domain.Term{Name: "related_party_payments", Kind: domain.TermRelatedPartyPayments,
			Line: "платежи в пользу связанных сторон"})

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Confidence <= 0.2 {
		t.Errorf("confidence = %v: a dossier with no related parties is an answer, not a gap", v.Confidence)
	}
}

func TestScopedTermStillFollowsTheAuditorWhenItCannotNarrow(t *testing.T) {
	build := func(scope string) *Inputs {
		return &Inputs{
			Facts: &domain.FactBase{Adjustments: []domain.Adjustment{{
				Kind: domain.AdjReclassify, Counterparty: "Advisory Bureau",
				Amount: dec("400000"), FromCategory: "Консультационные услуги",
				ToCategory: "Операционные расходы", Applied: true,
			}}},
			Labels: &domain.LabelSet{Txns: []domain.TxnLabel{label("TXN-P1-0001", domain.CatOperatingCosts)}},
			Txns:   []domain.Txn{ledgerRow("TXN-P1-0001", 10, "Contractor", "-1000000")},
		}
	}
	plain := spec("opex", "<=", "1200000", term("opex", "Операционные расходы"))
	scoped := spec("opex", "<=", "1200000",
		domain.Term{Name: "opex", Kind: domain.TermStatementLine,
			Line: "Операционные расходы", EntityScope: domain.StatusRestricted})

	want, err := Evaluate(plain, build(""))
	if err != nil {
		t.Fatalf("Evaluate plain: %v", err)
	}
	got, err := Evaluate(scoped, build(domain.StatusRestricted))
	if err != nil {
		t.Fatalf("Evaluate scoped: %v", err)
	}
	if !got.Actual.Equal(want.Actual) {
		t.Errorf("actual = %s, want %s: a scope nothing answers must not drop the reclassification",
			got.Actual, want.Actual)
	}
	if got.Status != want.Status {
		t.Errorf("status = %s, want %s", got.Status, want.Status)
	}
	if got.Confidence > 0.2 {
		t.Errorf("confidence = %v, want it dropped for a scope that could not be applied", got.Confidence)
	}
}

func TestScopedTermCountsOnlyTheCounterpartiesInScope(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Parties: []domain.Party{
			{Name: "Inside Holdings LLP", Status: domain.StatusUnrestricted},
			{Name: "Outside Holdings LLP", Status: domain.StatusRestricted},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatAssetTransfer),
			label("TXN-P1-0002", domain.CatAssetTransfer),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Inside Holdings LLP", "-300000"),
			ledgerRow("TXN-P1-0002", 20, "Outside Holdings LLP", "-700000"),
		},
	}
	s := spec("transferred", "<=", "1000000",
		domain.Term{Name: "transferred", Kind: domain.TermLedgerCategory,
			Line:        "капитальные активы, переданные дочерним организациям",
			EntityScope: domain.StatusUnrestricted})

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("300000")) {
		t.Errorf("actual = %s, want 300000: only the unrestricted counterparty counts", v.Actual)
	}
	if v.Confidence <= 0.2 {
		t.Errorf("confidence = %v: the scope was applied, nothing is missing", v.Confidence)
	}
}

func TestATermIsUnmeasurableWhileARowHasNoAmount(t *testing.T) {
	blank := ledgerRow("TXN-P1-0033", 20, "State Revenue Committee", "0")
	blank.AmountMissing = true

	in := &Inputs{
		Facts: &domain.FactBase{},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0010", domain.CatTaxes),
			label("TXN-P1-0033", domain.CatTaxes),
		}},
		Txns: []domain.Txn{ledgerRow("TXN-P1-0010", 10, "Tax Office", "-402118.64"), blank},
	}
	s := spec("taxes", "<=", "500000", term("taxes", "Налоги"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Confidence > 0.2 {
		t.Errorf("confidence = %v, want it dropped while a row of the term has no amount", v.Confidence)
	}
	if !slices.ContainsFunc(v.Trace, func(l string) bool { return strings.Contains(l, "TXN-P1-0033") }) {
		t.Errorf("the trace must name the unpriced row: %q", v.Trace)
	}
	if !v.Actual.Equal(dec("402118.64")) {
		t.Errorf("actual = %s: the priced rows are still summed", v.Actual)
	}
}

func TestTheAuditorsFigureMakesTheTermMeasurableAgain(t *testing.T) {
	blank := ledgerRow("TXN-P1-0033", 20, "State Revenue Committee", "0")
	blank.AmountMissing = true

	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{{
			Kind: domain.AdjLedgerAmountFix, TxnID: "TXN-P1-0033",
			Amount: dec("486204.19"), Applied: true,
		}}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0010", domain.CatTaxes),
			label("TXN-P1-0033", domain.CatTaxes),
		}},
		Txns: []domain.Txn{ledgerRow("TXN-P1-0010", 10, "Tax Office", "-402118.64"), blank},
	}
	s := spec("taxes", "<=", "500000", term("taxes", "Налоги"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("888322.83")) {
		t.Errorf("actual = %s, want 888322.83: the disclosed figure joins the sum", v.Actual)
	}
	if v.Status != domain.StatusBreach {
		t.Errorf("status = %s, want BREACH", v.Status)
	}
	if v.Confidence <= 0.2 {
		t.Errorf("confidence = %v: nothing is missing once the auditor states the figure", v.Confidence)
	}
}

func TestBothSpellingsOfAdjustedEbitdaAgree(t *testing.T) {
	build := func() *Inputs {
		return &Inputs{
			Facts: &domain.FactBase{Adjustments: []domain.Adjustment{{
				Kind: domain.AdjEBITDAAddBack, Amount: dec("481247.63"), Applied: true,
			}}},
			Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
				label("TXN-P1-0001", domain.CatRevenue),
				label("TXN-P1-0002", domain.CatOperatingCosts),
				label("TXN-P1-0003", domain.CatOperatingCosts),
			}},
			Txns: []domain.Txn{
				ledgerRow("TXN-P1-0001", 10, "Customer", "7004318.47"),
				ledgerRow("TXN-P1-0002", 20, "Contractor", "-4683001.13"),
				ledgerRow("TXN-P1-0003", 30, "Restoration Works", "-481247.63"),
			},
		}
	}
	spelledOut := spec("(revenue - opex + one_off_items) / revenue", ">=", "0.28",
		term("revenue", "Выручка"),
		term("opex", "Операционные расходы"),
		domain.Term{Name: "one_off_items", Kind: domain.TermStatementNote, Line: "разовые статьи"})
	named := spec("ebitda / revenue", ">=", "0.28",
		term("ebitda", "Скорректированная EBITDA"),
		term("revenue", "Выручка"))

	long, err := Evaluate(spelledOut, build())
	if err != nil {
		t.Fatalf("Evaluate spelled out: %v", err)
	}
	short, err := Evaluate(named, build())
	if err != nil {
		t.Fatalf("Evaluate named: %v", err)
	}
	if !short.Actual.Equal(long.Actual) {
		t.Errorf("actual %s vs %s: one covenant written two ways must give one number",
			short.Actual, long.Actual)
	}
	if short.Status != long.Status {
		t.Errorf("status %s vs %s", short.Status, long.Status)
	}
}

func TestAnAddBackIsNotCountedTwice(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{{
			Kind: domain.AdjEBITDAAddBack, Amount: dec("400000"), Applied: true,
		}}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "3000000"),
			ledgerRow("TXN-P1-0002", 20, "Contractor", "-400000"),
		},
	}
	s := spec("ebitda + one_off_items", ">=", "0",
		term("ebitda", "EBITDA"),
		domain.Term{Name: "one_off_items", Kind: domain.TermStatementNote, Line: "разовые статьи"})

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !v.Actual.Equal(dec("3000000")) {
		t.Errorf("actual = %s, want 3000000: the add-back belongs to the term that names it", v.Actual)
	}
}
