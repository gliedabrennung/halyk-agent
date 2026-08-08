package covenants

import (
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

// Normalise доводит спеку до вида, который понимает движок, и возвращает список того, что
// пришлось поправить или восстановить. Вызывается сразу после извлечения и снова при
// загрузке из стора, потому что в сторе лежат спеки, сохранённые до появления
// Term.EntityScope: без восстановления они посчитались бы без сужения по статусу
// контрагента и молча дали бы другое число.
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
			t.EntityScope, t.ScopeInferred = "", false
		}
		if t.EntityScope != "" {
			continue
		}
		if scope, ok := legacyEntityScope(*t); ok {
			t.EntityScope, t.ScopeInferred = scope, true
			notes = append(notes, fmt.Sprintf(
				"term %q: entity_scope %q read off the clause wording (specification predates the field)",
				t.Name, scope))
		}
	}
	return notes
}

// legacyEntityScope — единственное место, где статус контрагента берётся из формулировки
// пункта, и существует только для спек, извлечённых до появления entity_scope. Свежая
// спека сюда не попадает: у неё поле заполнено моделью. Удалить, когда стор переизвлечён.
//
// Распознаётся только «неограниченные» — то есть выведенные из-под обеспечения. Обратное
// («ограниченные») из текста читать нельзя: в этих договорах «Ограниченные платежи» —
// устоявшееся название платежей в пользу связанных сторон, а не статус дочерней
// организации. Движок принимает restricted как объявленный скоуп, но не угадывает его.
var _legacyUnrestricted = []string{"неограниченн", "unrestricted"}

func legacyEntityScope(t domain.Term) (string, bool) {
	switch t.Kind {
	case domain.TermRelatedPartyPayments, domain.TermGroupConsolidated,
		domain.TermStatementNote, domain.TermConstant:

		return "", false
	}
	blob := strings.ToLower(t.Line + " " + t.Description)
	for _, needle := range _legacyUnrestricted {
		if strings.Contains(blob, needle) {
			return domain.StatusUnrestricted, true
		}
	}
	return "", false
}
