package domain

import "testing"

// Строка, в которой сходятся два признака, разрешается по самому длинному совпадению, а не по
// порядку записей в таблице. Иначе перестановка таблицы молча меняла бы категорию.
func TestCategoryForLinePrefersTheLongestMatch(t *testing.T) {
	cases := []struct {
		line string
		want Category
	}{
		{"совокупная стоимость капитальных активов, переданных дочерним организациям", CatAssetTransfer},
		{"Капитальные затраты", CatCapex},
		{"Операционные расходы", CatOperatingCosts},
		{"все расходы на оплату труда, отражённые в операционных записях", CatPayroll},
		{"Выручка", CatRevenue},
		{"operating_costs", CatOperatingCosts},
	}
	for _, c := range cases {
		got, ok := CategoryForLine(c.line)
		if !ok || got != c.want {
			t.Errorf("CategoryForLine(%q) = %q, %v; want %q", c.line, got, ok, c.want)
		}
	}
}

func TestCategoryForLineRejectsWhatItCannotMap(t *testing.T) {
	for _, line := range []string{"", "   ", "нечто неопознанное", "not_a_category"} {
		if got, ok := CategoryForLine(line); ok {
			t.Errorf("CategoryForLine(%q) = %q, true; want no match", line, got)
		}
	}
}

// Порядок записей в таблице псевдонимов не должен влиять на результат.
func TestCategoryForLineIsIndependentOfAliasOrder(t *testing.T) {
	const line = "капитальных активов, переданных дочерним организациям"
	want, _ := CategoryForLine(line)

	original := _lineAliases
	defer func() { _lineAliases = original }()

	reversed := make([]struct {
		needle string
		cat    Category
	}, len(original))
	for i, a := range original {
		reversed[len(original)-1-i] = a
	}
	_lineAliases = reversed

	if got, _ := CategoryForLine(line); got != want {
		t.Errorf("reversing the alias table changed the answer: %q -> %q", want, got)
	}
}
