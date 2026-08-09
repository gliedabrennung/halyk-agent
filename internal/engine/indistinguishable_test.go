package engine

import (
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func relatedPartyInputs() *Inputs {
	return &Inputs{
		ScenarioID: "P1",
		Facts: &domain.FactBase{ScenarioID: "P1", Parties: []domain.Party{
			{Name: "Altyn Capital LLP", Related: true, Declared: true},
		}},
		Labels: &domain.LabelSet{ScenarioID: "P1", Txns: []domain.TxnLabel{
			{TxnID: "TXN-P1-0001", Category: domain.CatOperatingCosts, RelatedParty: true, PartyName: "Altyn Capital LLP"},
			{TxnID: "TXN-P1-0002", Category: domain.CatOperatingCosts, RelatedParty: true, PartyName: "Altyn Capital LLP"},
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Altyn Capital LLP", "-300000"),
			ledgerRow("TXN-P1-0002", 20, "Altyn Capital LLP", "-200000"),
		},
	}
}

func relatedTerm(name string) domain.Term {
	return domain.Term{Name: name, Kind: domain.TermRelatedPartyPayments}
}

// Two names over one derivation say no more than one name does, and the
// difference between them is a figure no document states.
func TestTermsDerivedIdenticallyAreUnmeasurable(t *testing.T) {
	s := spec("payments - basket", "<=", "250000",
		relatedTerm("payments"), relatedTerm("basket"))

	v, err := Evaluate(s, relatedPartyInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Confidence > 0.2 {
		t.Errorf("confidence = %v, want the unmeasurable cap 0.2: both terms sum the same rows the same way",
			v.Confidence)
	}
	joined := strings.Join(v.Trace, "\n")
	if !strings.Contains(joined, "payments and basket") {
		t.Errorf("trace does not name the two terms it cannot tell apart:\n%s", joined)
	}
}

// The guard must not fire on terms that merely happen to be equal: it reports
// an identical derivation, not an identical number.
func TestTermsFromDifferentRowsStayMeasurable(t *testing.T) {
	in := &Inputs{
		ScenarioID: "P1",
		Facts:      &domain.FactBase{ScenarioID: "P1"},
		Labels: &domain.LabelSet{ScenarioID: "P1", Txns: []domain.TxnLabel{
			label("TXN-P1-0001", domain.CatRevenue),
			label("TXN-P1-0002", domain.CatOperatingCosts),
		}},
		Txns: []domain.Txn{
			ledgerRow("TXN-P1-0001", 10, "Customer", "500000"),
			ledgerRow("TXN-P1-0002", 20, "Contractor", "-500000"),
		},
	}
	s := spec("revenue - opex", "<=", "250000",
		term("revenue", "Выручка"), term("opex", "Операционные расходы"))

	v, err := Evaluate(s, in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.Confidence <= 0.2 {
		t.Errorf("confidence = %v: equal totals from different categories are a real zero, not an ambiguity",
			v.Confidence)
	}
}
