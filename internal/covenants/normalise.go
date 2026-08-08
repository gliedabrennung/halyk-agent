package covenants

import (
	"fmt"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

// Normalise проверяет поля спеки, которые движок читает на веру, и возвращает список того, что
// пришлось поправить. Вызывается сразу после извлечения и снова при загрузке из стора: спека
// приходит от модели, и негодное значение лучше снять один раз в известном месте, чем узнавать
// о нём по расхождению в числе.
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
