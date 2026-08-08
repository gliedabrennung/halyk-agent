package covenants

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestNormaliseBackfillsAnUnrestrictedScope(t *testing.T) {
	spec := &domain.CovenantSpec{ScenarioID: "P9", ClauseID: "6.1", Terms: []domain.Term{{
		Name: "asset_transfers",
		Kind: domain.TermLedgerCategory,
		Line: "совокупная стоимость капитальных активов, переданных Неограниченным дочерним организациям",
	}}}

	notes := Normalise(spec)
	if len(notes) != 1 {
		t.Fatalf("the backfill must be reported, got %v", notes)
	}
	got := spec.Terms[0]
	if got.EntityScope != domain.StatusUnrestricted {
		t.Errorf("entity_scope = %q, want %q", got.EntityScope, domain.StatusUnrestricted)
	}
	if !got.ScopeInferred {
		t.Error("a backfilled scope must be marked as inferred")
	}
}

// «Ограниченные платежи» — устоявшееся название платежей связанным сторонам, а не статус
// дочерней организации. Из текста этот скоуп не читается ни при каких условиях.
func TestNormaliseLeavesRelatedPartyTermsAlone(t *testing.T) {
	spec := &domain.CovenantSpec{ScenarioID: "P2", ClauseID: "6.3", Terms: []domain.Term{{
		Name: "related_party_payments",
		Kind: domain.TermRelatedPartyPayments,
		Line: "Ограниченные платежи в пользу аффилированных лиц",
	}}}

	if notes := Normalise(spec); len(notes) != 0 {
		t.Fatalf("nothing should be inferred here, got %v", notes)
	}
	if spec.Terms[0].EntityScope != "" {
		t.Errorf("entity_scope = %q, want empty", spec.Terms[0].EntityScope)
	}
}

func TestNormaliseKeepsADeclaredScopeAndDropsAnInvalidOne(t *testing.T) {
	spec := &domain.CovenantSpec{Terms: []domain.Term{
		{Name: "declared", Kind: domain.TermLedgerCategory, EntityScope: domain.StatusRestricted},
		{Name: "nonsense", Kind: domain.TermLedgerCategory, EntityScope: "affiliated", Line: "аренда"},
	}}

	notes := Normalise(spec)
	if spec.Terms[0].EntityScope != domain.StatusRestricted || spec.Terms[0].ScopeInferred {
		t.Errorf("a declared scope must survive untouched: %+v", spec.Terms[0])
	}
	if spec.Terms[1].EntityScope != "" {
		t.Errorf("entity_scope = %q, want it dropped", spec.Terms[1].EntityScope)
	}
	if len(notes) != 1 {
		t.Errorf("the dropped value must be reported, got %v", notes)
	}
}

// Терм без всякого упоминания статуса остаётся без сужения: движок считает по всем строкам.
func TestNormaliseDoesNothingToAPlainTerm(t *testing.T) {
	spec := &domain.CovenantSpec{Terms: []domain.Term{
		{Name: "capex", Kind: domain.TermStatementLine, Line: "Капитальные затраты"},
	}}
	if notes := Normalise(spec); len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if spec.Terms[0].EntityScope != "" {
		t.Errorf("entity_scope = %q, want empty", spec.Terms[0].EntityScope)
	}
}
