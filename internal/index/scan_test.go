package index

import (
	"strings"
	"testing"
)

const _agreementExcerpt = `
                        Статья 6 — Финансовые ковенанты

Пункт 6.1 Maximum Capital Intensity Ratio. Заёмщик, Aktau Port Services JSC, обязуется не
допускать, чтобы коэффициент капиталоёмкости за период с 2025-01-01 по 2025-12-31
превышал 0.42x. Коэффициент капиталоёмкости означает отношение совокупных капитальных
затрат за период к сумме операционных расходов.

Пункт 6.2 Restricted Payments. Общая сумма ограниченных платежей не должна превышать
USD 5,000,000 за Ковенантный период.

Пункт 6.3 Single Related-Party Transaction. Ни одна операция со связанной стороной не
должна превышать EUR 300,000.
`

func TestScanFindsClauseNumbersAfterKeyword(t *testing.T) {
	s := ScanText(_agreementExcerpt, nil)
	for _, want := range []string{"6.1", "6.2", "6.3"} {
		if !contains(s.ClauseNumbers, want) {
			t.Errorf("clause %s not found; got %v", want, s.ClauseNumbers)
		}
	}
	if !s.HasClauses([]string{"6.1", "6.2", "6.3"}) {
		t.Error("HasClauses should be true for the three covenants present")
	}
	if s.HasClauses([]string{"6.1", "6.9"}) {
		t.Error("HasClauses must be false when one clause is absent")
	}
}

func TestScanIgnoresDecimalsThatLookLikeClauses(t *testing.T) {
	s := ScanText("превышал 0.42x.\n1.55 kg of material\n", nil)
	if contains(s.ClauseNumbers, "0.42") {
		t.Errorf("0.42 was read as a clause number: %v", s.ClauseNumbers)
	}
}

func TestScanHeadingStyleClauses(t *testing.T) {
	s := ScanText("6.1  Maximum Leverage\n6.2) Restricted Payments\n", nil)
	for _, want := range []string{"6.1", "6.2"} {
		if !contains(s.ClauseNumbers, want) {
			t.Errorf("heading clause %s not found; got %v", want, s.ClauseNumbers)
		}
	}
}

func TestScanAccountsCurrenciesPeriod(t *testing.T) {
	s := ScanText(_agreementExcerpt+"\nбанковский счёт ACC-7801 у Кредитора, счёт ACC-7801 повторно\n", []string{"ACC-7801"})
	if len(s.AccountIDs) != 1 || s.AccountIDs[0] != "ACC-7801" {
		t.Errorf("account ids = %v, want [ACC-7801] deduplicated", s.AccountIDs)
	}
	if !contains(s.Currencies, "USD") || !contains(s.Currencies, "EUR") {
		t.Errorf("currencies = %v, want USD and EUR", s.Currencies)
	}
	if s.PeriodFrom != "2025-01-01" || s.PeriodTo != "2025-12-31" {
		t.Errorf("period = %s..%s, want 2025-01-01..2025-12-31", s.PeriodFrom, s.PeriodTo)
	}
	if !s.CoversYear("2025") {
		t.Error("CoversYear(2025) should be true")
	}
	if s.CoversYear("2024") {
		t.Error("CoversYear(2024) should be false")
	}
}

func TestScanDetectsSupersededBanner(t *testing.T) {
	text := "НЕДЕЙСТВУЮЩАЯ РЕДАКЦИЯ (2024 г.). Заменена и изложена в новой редакции\n" + _agreementExcerpt
	s := ScanText(text, nil)
	if !s.Superseded {
		t.Fatal("the superseded banner was not detected")
	}
	if !strings.Contains(s.SupersededQuote, "НЕДЕЙСТВУЮЩАЯ") {
		t.Errorf("quote = %q, want it to include the banner", s.SupersededQuote)
	}
}

func TestScanCleanDocumentIsNotSuperseded(t *testing.T) {
	if ScanText(_agreementExcerpt, nil).Superseded {
		t.Error("a current agreement must not be flagged as superseded")
	}
}

func TestNormaliseCompany(t *testing.T) {
	tests := []struct{ a, b string }{
		{"Aktau Port Services JSC", "aktau port services"},
		{"Aktau Holdings L.L.P.", "aktau holdings"},
		{`"Taraz Holding Group" LLP`, "taraz holding group"},
		{"  Ural  Grinding   Works LLP ", "ural grinding works"},
	}
	for _, tt := range tests {
		if got := normaliseCompany(tt.a); got != tt.b {
			t.Errorf("normaliseCompany(%q) = %q, want %q", tt.a, got, tt.b)
		}
	}
	if normaliseCompany("") != "" {
		t.Error("an empty name must normalise to empty, not match everything")
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestScanIgnoresVersioningProseOutsideTheBanner(t *testing.T) {
	body := strings.Repeat("Обычный текст документа. ", 120) +
		"\nПредыдущая редакция сохраняется в архиве и помечается как недействующая.\n"
	if s := ScanText(body, nil); s.Superseded {
		t.Errorf("a policy describing archiving was read as retired: %q", s.SupersededQuote)
	}
}

func TestScanReadsBannerAtTheTop(t *testing.T) {
	text := "   НЕДЕЙСТВУЮЩАЯ РЕДАКЦИЯ (2024 г.). Заменена.\n\n" + _agreementExcerpt
	if !ScanText(text, nil).Superseded {
		t.Error("a banner above the title must be detected")
	}
}

func TestScanPrefersTheNamedCovenantPeriod(t *testing.T) {
	text := `Аудит проводился с 2026-01-15 по 2026-03-20.
Заёмщик обязан соблюдать ковенанты в течение Ковенантного периода с 2025-01-01 по 2025-12-31.`
	s := ScanText(text, nil)
	if s.PeriodFrom != "2025-01-01" || s.PeriodTo != "2025-12-31" {
		t.Errorf("period = %s..%s, want the covenant period 2025-01-01..2025-12-31", s.PeriodFrom, s.PeriodTo)
	}
	if !s.PeriodIsCovenant {
		t.Error("the period should be marked as a covenant period")
	}
}

func TestScanFallsBackToAnyPeriod(t *testing.T) {
	s := ScanText("Отчётный период с 2025-01-01 по 2025-12-31.", nil)
	if s.PeriodFrom != "2025-01-01" {
		t.Errorf("period_from = %q, want the generic range as a fallback", s.PeriodFrom)
	}
	if s.PeriodIsCovenant {
		t.Error("a generic range must not claim to be the covenant period")
	}
}

func TestScanFindsWhateverAccountIDsTheLedgerHolds(t *testing.T) {
	const text = "Договор со счётом ACC-7801, а также TELE-4471 и ACC-9999 упомянуты ниже."
	s := ScanText(text, []string{"ACC-7801", "TELE-4471", "ACC-1234"})

	for _, want := range []string{"ACC-7801", "TELE-4471"} {
		if !contains(s.AccountIDs, want) {
			t.Errorf("%s not found; got %v", want, s.AccountIDs)
		}
	}
	if contains(s.AccountIDs, "ACC-9999") {
		t.Errorf("ACC-9999 is in the text but not in the ledger, it must not be reported: %v", s.AccountIDs)
	}
	if contains(s.AccountIDs, "ACC-1234") {
		t.Errorf("ACC-1234 is in the ledger but not in the text: %v", s.AccountIDs)
	}
}
