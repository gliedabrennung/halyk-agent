package covenants

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestNormaliseKeepsADeclaredScopeAndDropsAnInvalidOne(t *testing.T) {
	spec := &domain.CovenantSpec{Terms: []domain.Term{
		{Name: "declared", Kind: domain.TermLedgerCategory, EntityScope: domain.StatusRestricted},
		{Name: "nonsense", Kind: domain.TermLedgerCategory, EntityScope: "affiliated", Line: "аренда"},
	}}

	notes := Normalise(spec)
	if spec.Terms[0].EntityScope != domain.StatusRestricted {
		t.Errorf("a declared scope must survive untouched: %+v", spec.Terms[0])
	}
	if spec.Terms[1].EntityScope != "" {
		t.Errorf("entity_scope = %q, want it dropped", spec.Terms[1].EntityScope)
	}
	if len(notes) != 1 {
		t.Errorf("the dropped value must be reported, got %v", notes)
	}
}

// Скоуп берётся только из спеки. Формулировка пункта его больше не задаёт — ни в ту сторону,
// ни в обратную, — поэтому терм, где статус лишь упомянут словами, остаётся без сужения.
func TestNormaliseNeverReadsAScopeOutOfTheWording(t *testing.T) {
	spec := &domain.CovenantSpec{Terms: []domain.Term{
		{
			Name: "asset_transfers", Kind: domain.TermLedgerCategory,
			Line: "совокупная стоимость капитальных активов, переданных Неограниченным дочерним организациям",
		},
		{
			Name: "related_party_payments", Kind: domain.TermRelatedPartyPayments,
			Line: "Ограниченные платежи в пользу аффилированных лиц",
		},
		{Name: "capex", Kind: domain.TermStatementLine, Line: "Капитальные затраты"},
	}}

	if notes := Normalise(spec); len(notes) != 0 {
		t.Fatalf("nothing should be inferred, got %v", notes)
	}
	for _, term := range spec.Terms {
		if term.EntityScope != "" {
			t.Errorf("%s picked up a scope from its wording: %+v", term.Name, term)
		}
	}
}
