package covenants

import (
	"fmt"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

// Normalise снимает негодные значения полей спеки и возвращает список поправок.
func Normalise(spec *domain.CovenantSpec) []string {
	if spec == nil {
		return nil
	}
	var notes []string
	if note := widenInstant(&spec.Period); note != "" {
		notes = append(notes, note)
	}
	for i := range spec.Terms {
		t := &spec.Terms[i]

		if !domain.ValidEntityScope(t.EntityScope) {
			notes = append(notes, fmt.Sprintf("term %q: entity_scope %q is not a party status; ignored",
				t.Name, t.EntityScope))
			t.EntityScope = ""
		}
	}
	return notes
}

func widenInstant(p *domain.Period) string {
	if p.From.IsZero() || !p.From.Equal(p.To) {
		return ""
	}
	p.From = time.Time{}
	return fmt.Sprintf(
		"period %s..%s has no width, and a ledger of flows holds nothing inside an instant; "+
			"reading it as everything up to that date",
		p.To.Format("2006-01-02"), p.To.Format("2006-01-02"))
}
