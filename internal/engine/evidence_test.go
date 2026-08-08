package engine

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestEvidenceIsTheRowThatDecidesTheVerdictNotTheLargest(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{
			RelatedPartyThreshold: dec("25"),
			Parties:               []domain.Party{{Name: "Ertis Capital LLP", VotingShare: dec("34.5"), Related: true}},
		},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			{TxnID: "TXN-P1-0002", Category: domain.CatProfessionalService,
				Counterparty: "Ertis Capital LLP", RelatedParty: true, PartyName: "Ertis Capital LLP"},
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "10000000"),
			ledgerRow("TXN-P1-0002", 200, "Ertis Capital LLP", "-300000"),
		},
	}
	s := spec("related_party_payments / revenue", "<=", "0.02",
		domain.Term{Name: "related_party_payments", Kind: domain.TermRelatedPartyPayments,
			Line: "платежи в пользу связанных сторон"},
		term("revenue", "Выручка"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusBreach {
		t.Fatalf("status = %s, want BREACH (0.03 > 0.02)", v.Status)
	}
	id, candidates := Evidence(s, in, v)
	if id == nil || *id != "TXN-P1-0002" {
		t.Fatalf("evidence = %v, want the 300,000 payment; candidates %v", id, candidates)
	}
}

func TestEvidenceSingleDecidingRow(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatCapex),
			label("TXN-P1-0002", domain.CatCapex),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Supplier", "-2500000"),
			ledgerRow("TXN-P1-0002", 350, "Workshop", "-100000"),
		},
	}
	s := spec("capex", "<=", "2400000", term("capex", "Капитальные затраты"))
	s.EvidenceKind = "single_txn"

	v, _ := Evaluate(s, in)
	id, _ := Evidence(s, in, v)
	if id == nil || *id != "TXN-P1-0001" {
		t.Fatalf("evidence = %v, want TXN-P1-0001", id)
	}
}

func TestEvidenceIsNilWhenSeveralRowsDecide(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatCapex),
			label("TXN-P1-0002", domain.CatCapex),
			label("TXN-P1-0003", domain.CatCapex),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "A", "-800000"),
			ledgerRow("TXN-P1-0002", 20, "B", "-800000"),
			ledgerRow("TXN-P1-0003", 30, "C", "-800000"),
		},
	}
	s := spec("capex", "<=", "2000000", term("capex", "Капитальные затраты"))
	s.EvidenceKind = "aggregate"

	v, _ := Evaluate(s, in)
	id, candidates := Evidence(s, in, v)
	if id != nil {
		t.Errorf("evidence = %v, want none", *id)
	}
	if len(candidates) != 3 {
		t.Errorf("candidates = %v, want all three rows", candidates)
	}
}

func TestEvidencePrefersTheReclassifiedRow(t *testing.T) {
	in := &Inputs{
		Facts: &domain.FactBase{Adjustments: []domain.Adjustment{
			{Kind: domain.AdjReclassify, Counterparty: "Tien Shan Advisory Bureau",
				Amount: dec("1104663.28"), FromCategory: "Консультационные услуги",
				ToCategory: "Операционные расходы", Applied: true},
		}},
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatOperatingCosts),
			{TxnID: "TXN-P1-0003", Category: domain.CatProfessionalService,
				Counterparty: "Tien Shan Advisory Bureau", Reclassified: true,
				ReclassifiedTo: domain.CatOperatingCosts, AdjustmentKind: domain.AdjReclassify},
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "7214663.82"),
			ledgerRow("TXN-P1-0002", 20, "Contractor", "-5918004.37"),
			ledgerRow("TXN-P1-0003", 30, "Tien Shan Advisory Bureau", "-1104663.28"),
		},
	}
	s := spec("revenue / opex", ">=", "1.15",
		term("revenue", "Выручка"), term("opex", "Операционные затраты"))
	s.EvidenceKind = "aggregate"

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Status != domain.StatusBreach {
		t.Fatalf("status = %s, want BREACH (7214663.82 / 7022667.65 = 1.027)", v.Status)
	}
	id, candidates := Evidence(s, in, v)
	if len(candidates) < 2 {
		t.Fatalf("candidates = %v, want more than one", candidates)
	}
	if id == nil || *id != "TXN-P1-0003" {
		t.Fatalf("evidence = %v, want the reclassified row TXN-P1-0003", id)
	}
}

func TestEvidenceIgnoresRowsThatDestroyTheMetric(t *testing.T) {
	in := &Inputs{
		Labels: &domain.LabelSet{Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "1000000"),
			ledgerRow("TXN-P1-0002", 20, "Contractor", "-2000000"),
		},
	}
	s := spec("revenue / opex", ">=", "1", term("revenue", "Выручка"), term("opex", "Операционные расходы"))
	s.EvidenceKind = "aggregate"

	v, _ := Evaluate(s, in)
	if v.Status != domain.StatusBreach {
		t.Fatalf("status = %s, want BREACH", v.Status)
	}
	id, candidates := Evidence(s, in, v)

	if id != nil {
		t.Errorf("evidence = %v, want none", *id)
	}
	for _, c := range candidates {
		if c == "TXN-P1-0002" {
			t.Error("the denominator's only row must not count as a candidate")
		}
	}
}

func TestGroupCapexComesFromThePPEMovement(t *testing.T) {
	ppe := &domain.GroupPPE{
		Opening:         dec("148028989.69"),
		Closing:         dec("154050122.81"),
		Depreciation:    dec("15826229.43"),
		DisposalsStated: true,
	}
	capex, ok := ppe.Capex()
	if !ok {
		t.Fatal("a stated no-disposal movement must yield a figure")
	}
	if want := dec("21847362.55"); !capex.Equal(want) {
		t.Errorf("capex = %s, want %s", capex, want)
	}
}

func TestGroupCapexNeedsDisposalsPinnedDown(t *testing.T) {
	silent := &domain.GroupPPE{
		Opening:      dec("100"),
		Closing:      dec("120"),
		Depreciation: dec("10"),
	}
	if _, ok := silent.Capex(); ok {
		t.Error("a note silent on disposals cannot pin the additions down")
	}

	stated := *silent
	stated.DisposalsStated = true
	stated.Disposals = dec("5")
	capex, ok := stated.Capex()
	if !ok {
		t.Fatal("stated disposals must yield a figure")
	}
	if want := dec("35"); !capex.Equal(want) {
		t.Errorf("capex = %s, want %s (disposals add back to the movement)", capex, want)
	}

	var absent *domain.GroupPPE
	if _, ok := absent.Capex(); ok {
		t.Error("no statements means no figure")
	}
}
