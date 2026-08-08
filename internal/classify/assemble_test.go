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
