package covenants

import (
	"fmt"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

// Normalise снимает негодные значения полей спеки и возвращает список поправок.
func Normalise(spec *domain.CovenantSpec) []string {
	if spec == nil {
		return nil
	}
	var notes []string
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
