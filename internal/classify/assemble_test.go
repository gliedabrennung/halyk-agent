package classify

import (
	"testing"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func txn(id, counterparty, description, amount string) *domain.Txn {
	return &domain.Txn{
		ID:           id,
		ScenarioID:   "P1",
		AccountID:    "ACC-1",
		Date:         time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		Counterparty: counterparty,
		Description:  description,
		Amount:       dec(amount),
		Currency:     "USD",
	}
}

// blankAmountTxn — строка, у которой выгрузка не дала суммы; её и чинит ledger_amount_fix.
func blankAmountTxn(id, counterparty, description string) *domain.Txn {
	t := txn(id, counterparty, description, "0")
	t.AmountMissing = true
	return t
}

func labels(l ...domain.Label) map[string]domain.Label {
	m := make(map[string]domain.Label, len(l))
	for _, x := range l {
		m[x.Pattern] = x
	}
	return m
}

func TestAssembleFlagsRelatedPartyAcrossSpellings(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID:            "P1",
		Company:               "Aktau Port Services JSC",
		RelatedPartyThreshold: dec("20"),
		Parties: []domain.Party{
			{Name: "Aktau Holdings LLP", VotingShare: dec("34.5"), Related: true},
			{Name: "Kaspi Marine Engineering LLP", VotingShare: dec("18.7"), Related: false},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0031", "Aktau Holdings L.L.P.", "Management advisory retainer", "-283664.18"),
		txn("TXN-P1-0045", "Kaspi Marine Engineering LLP", "Advisory engagement on tariff structuring", "-612884.19"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "management advisory retainer", Category: domain.CatProfessionalService, Source: "rule"},
		domain.Label{Pattern: "advisory engagement on tariff structuring", Category: domain.CatProfessionalService, Source: "llm"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	rp, _ := set.Lookup("TXN-P1-0031")
	if !rp.RelatedParty {
		t.Error("Aktau Holdings L.L.P. must resolve to the dossier's Aktau Holdings LLP")
	}
	if !rp.VotingShare.Equal(dec("34.5")) {
		t.Errorf("voting share = %s, want 34.5", rp.VotingShare)
	}

	other, _ := set.Lookup("TXN-P1-0045")
	if other.RelatedParty {
		t.Error("a party below the threshold must not be flagged as related")
	}
	if len(set.RelatedParties) != 1 || set.RelatedParties[0] != "Aktau Holdings LLP" {
		t.Errorf("related parties = %v", set.RelatedParties)
	}
}

func TestAssembleWarnsOnRelatedPartyWithNoLedgerRow(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Parties:    []domain.Party{{Name: "Ghost Holdings LLP", VotingShare: dec("51"), Related: true}},
	}
	txns := []*domain.Txn{txn("TXN-P1-0001", "Northwind Catering", "Office rent — Almaty", "-1000")}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(set.UnmatchedParties) != 1 {
		t.Fatalf("unmatched parties = %v", set.UnmatchedParties)
	}
	if len(warnings) != 1 {
		t.Fatalf("an unmatched related party must be reported, got %v", warnings)
	}
}

func TestAssembleAppliesContraOnlyToInflows(t *testing.T) {
	fb := &domain.FactBase{ScenarioID: "P1"}
	txns := []*domain.Txn{
		txn("TXN-P1-0002", "Acme", "Marketing volume rebate — Q2", "44000"),
		txn("TXN-P1-0003", "Acme", "Marketing volume rebate — Q3 clawback", "-9000"),
	}
	set, _, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "marketing volume rebate", Category: domain.CatMarketing, Contra: true, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	in, _ := set.Lookup("TXN-P1-0002")
	out, _ := set.Lookup("TXN-P1-0003")
	if !in.Contra {
		t.Error("the inflow reverses a cost and must carry contra")
	}
	if out.Contra {
		t.Error("an outflow cannot be a reversal")
	}
	if !set.Totals[domain.CatMarketing].Equal(dec("35000")) {
		t.Errorf("marketing total = %s, want 35000 (signed sum)", set.Totals[domain.CatMarketing])
	}
}

func TestAssembleNeverMarksAnUnpricedRowAsReversal(t *testing.T) {
	fb := &domain.FactBase{ScenarioID: "P1"}
	blank := txn("TXN-P1-0033", "State Revenue Committee", "Mineral extraction tax assessment 2025", "0")
	blank.AmountMissing = true
	set, _, err := assemble("P1", fb, []*domain.Txn{blank}, labels(
		domain.Label{Pattern: "mineral extraction tax assessment", Category: domain.CatTaxes, Contra: true, Source: "escalated"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	row, _ := set.Lookup("TXN-P1-0033")
	if row.Contra {
		t.Error("a row with no amount has no sign and cannot be a reversal")
	}
}

func TestAssembleMarksButDoesNotApplyReclassification(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{
				Kind:         domain.AdjReclassify,
				Counterparty: "Tien Shan Advisory Bureau",
				Amount:       dec("1104663.28"),
				FromCategory: "Консультационные услуги",
				ToCategory:   "Операционные расходы",
				Applied:      true,
			},
			{
				Kind:    domain.AdjNoChange,
				TxnID:   "TXN-P1-0009",
				Applied: false,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0008", "Tien Shan Advisory Bureau", "Advisory engagement on depot operations", "-1104663.28"),
		txn("TXN-P1-0009", "Northwind Catering", "Office rent — Almaty", "-1000"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "advisory engagement on depot operations", Category: domain.CatProfessionalService, Source: "llm"},
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	moved, _ := set.Lookup("TXN-P1-0008")
	if !moved.Reclassified {
		t.Fatal("the reclassified row must be marked")
	}
	if moved.Category != domain.CatProfessionalService {
		t.Errorf("category = %s; the label must keep the borrower's own booking", moved.Category)
	}
	if moved.ReclassifiedTo != domain.CatOperatingCosts {
		t.Errorf("reclassified_to = %q, want %q", moved.ReclassifiedTo, domain.CatOtherOperating)
	}

	rejected, _ := set.Lookup("TXN-P1-0009")
	if rejected.AdjustmentKind != domain.AdjNoChange {
		t.Errorf("adjustment kind = %q, want %q", rejected.AdjustmentKind, domain.AdjNoChange)
	}
	if rejected.Reclassified {
		t.Error("a rejected adjustment must never mark a row as reclassified")
	}
}

func TestAssembleLeavesAmbiguousAdjustmentUnmarked(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{Kind: domain.AdjReclassify, Counterparty: "Acme LLP", Applied: true, ToCategory: "Операционные расходы"},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0004", "Acme LLP", "Office rent — north wing", "-1000"),
		txn("TXN-P1-0005", "Acme L.L.P.", "Office rent — south wing", "-2000"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the ambiguity must be reported, got %v", warnings)
	}
	for _, tl := range set.Txns {
		if tl.Reclassified {
			t.Errorf("%s was marked from an ambiguous adjustment", tl.TxnID)
		}
	}
}

func TestAssembleFailsOnUnlabelledPattern(t *testing.T) {
	fb := &domain.FactBase{ScenarioID: "P1"}
	txns := []*domain.Txn{txn("TXN-P1-0006", "Acme", "Some novel wording", "-1")}
	if _, _, err := assemble("P1", fb, txns, labels()); err == nil {
		t.Fatal("a row whose pattern has no label must fail loudly, not default to a category")
	}
}

// Модель иногда выдумывает txn_id, которого в леджере нет (реальный случай: B1, TXN-B1-0634
// вместо TXN-B1-0020). Корректировку это терять не должно: контрагент и сумма на месте.
func TestAssembleFallsBackToCounterpartyWhenTheTxnIDIsInvented(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{
				Kind:         domain.AdjReclassify,
				TxnID:        "TXN-P1-0634",
				Counterparty: "Irtysh Advisory Bureau",
				Amount:       dec("592296.10"),
				ToCategory:   "Процентные расходы",
				Applied:      true,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0020", "Irtysh Advisory Bureau", "Advisory engagement on tariff structuring", "-592296.10"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "advisory engagement on tariff structuring", Category: domain.CatProfessionalService, Source: "llm"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the invented id must still be reported once, got %v", warnings)
	}

	moved, ok := set.Lookup("TXN-P1-0020")
	if !ok {
		t.Fatal("the row is missing from the label set")
	}
	if !moved.Reclassified {
		t.Error("the row must be marked from the counterparty match, not dropped with the bad id")
	}
	if moved.ReclassifiedTo != domain.CatInterestExpense {
		t.Errorf("reclassified_to = %q, want %q", moved.ReclassifiedTo, domain.CatInterestExpense)
	}
}

// Выдуманный id и контрагент, который ни с чем не сходится: пометить нечего.
func TestAssembleLeavesAnInventedTxnIDUnmarkedWhenNothingElseMatches(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{Kind: domain.AdjReclassify, TxnID: "TXN-P1-9999", Counterparty: "Nobody LLP", Applied: true},
		},
	}
	txns := []*domain.Txn{txn("TXN-P1-0004", "Acme LLP", "Office rent — north wing", "-1000")}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("both the bad id and the failed counterparty match must be reported, got %v", warnings)
	}
	for _, tl := range set.Txns {
		if tl.Reclassified || tl.AdjustmentKind != "" {
			t.Errorf("%s was marked from an adjustment that matches nothing", tl.TxnID)
		}
	}
}

// Текстовый слой документа исказил имя контрагента (реальный случай: P4, «ПеК Restoration
// Works LLP» вместо «Ilek Restoration Works LLP»). Сумма при этом цела и уникальна.
func TestAssembleMatchesOnAmountWhenTheNameIsCorrupted(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{
				Kind:         domain.AdjEBITDAAddBack,
				Counterparty: "ПеК Restoration Works LLP",
				Amount:       dec("481247.63"),
				Applied:      true,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0025", "Ilek Restoration Works LLP", "Flood remediation and silo repair works", "-481247.63"),
		txn("TXN-P1-0026", "Someone Else LLP", "Office rent — north wing", "-1000"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "flood remediation and silo repair works", Category: domain.CatOperatingCosts, Source: "rule"},
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the amount match must be reported once, got %v", warnings)
	}

	marked, _ := set.Lookup("TXN-P1-0025")
	if marked.AdjustmentKind != domain.AdjEBITDAAddBack {
		t.Errorf("adjustment kind = %q, want %q", marked.AdjustmentKind, domain.AdjEBITDAAddBack)
	}
	other, _ := set.Lookup("TXN-P1-0026")
	if other.AdjustmentKind != "" {
		t.Error("only the row carrying the amount may be marked")
	}
}

// Та же сумма у двух строк — опознать нечем, лучше не пометить ничего.
func TestAssembleWillNotGuessBetweenTwoRowsOfTheSameAmount(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{Kind: domain.AdjEBITDAAddBack, Counterparty: "Garbled Name LLP", Amount: dec("1000"), Applied: true},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0004", "Acme LLP", "Office rent — north wing", "-1000"),
		txn("TXN-P1-0005", "Beta LLP", "Office rent — south wing", "-1000"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the failure must be reported, got %v", warnings)
	}
	for _, tl := range set.Txns {
		if tl.AdjustmentKind != "" {
			t.Errorf("%s was marked on an ambiguous amount", tl.TxnID)
		}
	}
}

// Сумма сошлась, а название — чужое. В этом корпусе суммы внутри заёмщика уникальны, поэтому
// одной суммы «хватило бы»; именно так корректировка аудитора и привязалась бы к посторонней
// строке в реестре, где суммы повторяются. Название решает.
func TestAssembleWillNotMatchAnUnrelatedNameOnTheAmountAlone(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{
				Kind:         domain.AdjReclassify,
				Counterparty: "Tengiz Risk Engineering Bureau",
				Amount:       dec("142118.64"),
				ToCategory:   "операционные расходы",
				Applied:      true,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0009", "Saryarka Terminal Properties LLP", "Office rent — north wing", "-142118.64"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "office rent", Category: domain.CatRent, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the unmatched adjustment must be reported, got %v", warnings)
	}
	only, _ := set.Lookup("TXN-P1-0009")
	if only.AdjustmentKind != "" || only.Reclassified {
		t.Error("a row of another counterparty must not be reclassified because the amount coincides")
	}
}

func TestSimilarName(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Ilek Restoration Works LLP", "ПеК Restoration Works LLP", true},
		{"Zhaiyk Dredging LLP", "Halyk Dredging LLP", true},
		{"Aktau Holdings LLP", "Aktau Holdings L.L.P.", true},
		{"Tengiz Risk Engineering Bureau", "Saryarka Terminal Properties LLP", false},
		{"Acme LLP", "Beta LLP", false},
		{"Northwind Catering", "Northwind Catering (Taraz point)", true},
		{"LLP", "JSC", false},
	}
	for _, c := range cases {
		if got := similarName(c.a, c.b); got != c.want {
			t.Errorf("similarName(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Реальный случай B1: модель назвала txn_id, который существует, но принадлежит другой строке
// — другой контрагент, другой знак, другая сумма. Раскрытие при этом называет и контрагента, и
// сумму, и они однозначно указывают на настоящую строку. Существующий id не должен побеждать
// два сходящихся признака: так реклассификация аудитора садится на постороннюю строку молча.
func TestAssembleDistrustsATxnIDThatContradictsTheDisclosure(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{
				Kind:         domain.AdjReclassify,
				TxnID:        "TXN-P1-0001",
				Counterparty: "Irtysh Advisory Bureau",
				Amount:       dec("592296.10"),
				FromCategory: "консультационные услуги",
				ToCategory:   "процентные расходы",
				Applied:      true,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0001", "Bridgeport Property Ltd Service Centre", "Sublet rent received — February", "1656712.15"),
		txn("TXN-P1-0020", "Irtysh Advisory Bureau", "Advisory engagement on tariff structuring", "-592296.10"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "sublet rent received", Category: domain.CatRent, Contra: true, Source: "rule"},
		domain.Label{Pattern: "advisory engagement on tariff structuring", Category: domain.CatProfessionalService, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("the contradicting id must be reported once, got %v", warnings)
	}

	wrong, _ := set.Lookup("TXN-P1-0001")
	if wrong.Reclassified || wrong.AdjustmentKind != "" {
		t.Error("the named row contradicts the disclosure and must not be marked")
	}
	right, _ := set.Lookup("TXN-P1-0020")
	if !right.Reclassified || right.ReclassifiedTo != domain.CatInterestExpense {
		t.Errorf("the counterparty and the amount both point at TXN-P1-0020: %+v", right)
	}
}

// А непротиворечивый txn_id по-прежнему решает: он точнее имени и суммы.
func TestAssembleKeepsATxnIDThatAgreesWithTheDisclosure(t *testing.T) {
	fb := &domain.FactBase{
		ScenarioID: "P1",
		Adjustments: []domain.Adjustment{
			{Kind: domain.AdjExcludePeriod, TxnID: "TXN-P1-0045", Amount: dec("0"), Applied: true},
			{
				Kind: domain.AdjLedgerAmountFix, TxnID: "TXN-P1-0046",
				Amount: dec("884204.16"), Applied: true,
			},
		},
	}
	txns := []*domain.Txn{
		txn("TXN-P1-0045", "Aral Freight Arbitration Bureau", "Quay wall survey", "-612884.19"),
		blankAmountTxn("TXN-P1-0046", "Ural Crane Works LLP", "Crane servicing contract"),
	}
	set, warnings, err := assemble("P1", fb, txns, labels(
		domain.Label{Pattern: "quay wall survey", Category: domain.CatOperatingCosts, Source: "rule"},
		domain.Label{Pattern: "crane servicing contract", Category: domain.CatOperatingCosts, Source: "rule"},
	))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("both ids agree with their rows: %v", warnings)
	}
	for _, id := range []string{"TXN-P1-0045", "TXN-P1-0046"} {
		tl, _ := set.Lookup(id)
		if tl.AdjustmentKind == "" {
			t.Errorf("%s lost its adjustment", id)
		}
	}
}
