package covenants

import (
	"strings"
	"testing"
)

const _contractWithTOC = `
Halyk Bank of Kazakhstan JSC                              № ACC-7801

                            Содержание

Статья 1            Термины и толкование
Статья 5            Случаи неисполнения обязательств
Статья 6            Финансовые ковенанты
Статья 7            Ограничительные обязательства

Статья 1 — Термины и толкование
«Капитальные затраты» означают расходы на приобретение основных средств.
«Связанная сторона» имеет значение, придаваемое ему в МСФО (IAS) 24.

Статья 5 — Случаи неисполнения обязательств
Пункт 5.1 Неплатёж. Заёмщик не уплатил сумму в срок.

Статья 6 — Финансовые ковенанты
Пункт 6.1 Maximum Capital Intensity Ratio. Заёмщик обязуется не допускать, чтобы
коэффициент капиталоёмкости превышал 0.42x.

Пункт 6.2 Минимальная выручка по категории. Не менее $7,100,000.00.

Пункт 6.3 Максимальные платежи связанным сторонам. Не более $450,000.00.

Статья 7 — Ограничительные обязательства
Пункт 7.1 Отчуждение активов. Заёмщик не вправе отчуждать активы.
`

func TestArticleSkipsTheTableOfContents(t *testing.T) {
	sec, err := Article(_contractWithTOC, 6)
	if err != nil {
		t.Fatalf("Article: %v", err)
	}
	if !strings.Contains(sec.Text, "Пункт 6.1") {
		t.Fatalf("got the table-of-contents line, not the body:\n%s", sec.Text)
	}
	if strings.Contains(sec.Text, "Пункт 7.1") {
		t.Error("the section leaked into article 7")
	}
	if strings.Contains(sec.Text, "Пункт 5.1") {
		t.Error("the section leaked into article 5")
	}
	if sec.Number != 6 {
		t.Errorf("number = %d, want 6", sec.Number)
	}
}

func TestArticleMissing(t *testing.T) {
	if _, err := Article(_contractWithTOC, 12); err == nil {
		t.Error("a missing article must be an error, not an empty section")
	}
}

func TestClauseBoundaries(t *testing.T) {
	sec, err := Article(_contractWithTOC, 6)
	if err != nil {
		t.Fatalf("Article: %v", err)
	}
	c62, err := Clause(sec.Text, "6.2")
	if err != nil {
		t.Fatalf("Clause: %v", err)
	}
	if !strings.Contains(c62, "7,100,000") {
		t.Errorf("6.2 lost its threshold:\n%s", c62)
	}
	if strings.Contains(c62, "0.42x") || strings.Contains(c62, "450,000") {
		t.Errorf("6.2 bled into its neighbours:\n%s", c62)
	}

	c63, err := Clause(sec.Text, "6.3")
	if err != nil {
		t.Fatalf("Clause 6.3: %v", err)
	}
	if !strings.Contains(c63, "450,000") {
		t.Errorf("6.3 is wrong:\n%s", c63)
	}

	if _, err := Clause(sec.Text, "6.9"); err == nil {
		t.Error("a missing clause must be an error")
	}
}

func TestClauseIDs(t *testing.T) {
	sec, _ := Article(_contractWithTOC, 6)
	got := strings.Join(ClauseIDs(sec.Text), ",")
	if got != "6.1,6.2,6.3" {
		t.Errorf("ClauseIDs = %q, want 6.1,6.2,6.3", got)
	}
}

func TestCovenantArticleFor(t *testing.T) {
	sec, err := CovenantArticleFor(_contractWithTOC, []string{"6.1", "6.2", "6.3"})
	if err != nil {
		t.Fatalf("CovenantArticleFor: %v", err)
	}
	if sec.Number != 6 {
		t.Errorf("article = %d, want 6", sec.Number)
	}
}

func TestCovenantArticleForFindsAnotherArticle(t *testing.T) {
	text := `
Статья 4 — Финансовые ковенанты
Пункт 9.1 Первый ковенант. Не более $1.00.
Пункт 9.2 Второй ковенант. Не менее $2.00.

Статья 5 — Прочее
Пункт 5.1 Что-то ещё.
`
	sec, err := CovenantArticleFor(text, []string{"9.1", "9.2"})
	if err != nil {
		t.Fatalf("CovenantArticleFor: %v", err)
	}
	if sec.Number != 4 {
		t.Errorf("article = %d, want 4", sec.Number)
	}
}

func TestCovenantArticleForFailsLoudly(t *testing.T) {
	if _, err := CovenantArticleFor(_contractWithTOC, []string{"6.1", "8.7"}); err == nil {
		t.Error("a clause that exists nowhere must be an error, not a silently wrong section")
	}
}

func TestPageOf(t *testing.T) {
	text := "page one\fpage two\fpage three"
	tests := []struct {
		offset, want int
	}{
		{0, 1},
		{5, 1},
		{len("page one\f") + 1, 2},
		{len("page one\fpage two\f") + 1, 3},
	}
	for _, tt := range tests {
		if got := PageOf(text, tt.offset); got != tt.want {
			t.Errorf("PageOf(%d) = %d, want %d", tt.offset, got, tt.want)
		}
	}
}

func TestDefinitionsText(t *testing.T) {
	got := definitionsText(_contractWithTOC)
	if !strings.Contains(got, "Капитальные затраты") {
		t.Errorf("the definitions article was not extracted:\n%s", got)
	}
	if strings.Contains(got, "Пункт 6.1") {
		t.Error("the definitions section leaked into the covenants")
	}
}
