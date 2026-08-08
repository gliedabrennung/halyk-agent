package engine

import (
	"testing"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func txn(id, scn, amount string) domain.Txn {
	return domain.Txn{
		ID: id, ScenarioID: scn, AccountID: "ACC-1",
		Date:   time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		Amount: decimal.RequireFromString(amount), Currency: "USD",
	}
}

func TestMedianAbsExpense(t *testing.T) {
	tests := []struct {
		name string
		txns []domain.Txn
		want string
	}{
		{
			name: "odd count takes the middle",
			txns: []domain.Txn{txn("a", "P1", "-30"), txn("b", "P1", "-10"), txn("c", "P1", "-20")},
			want: "20",
		},
		{
			name: "even count averages the two middles",
			txns: []domain.Txn{txn("a", "P1", "-10"), txn("b", "P1", "-20"), txn("c", "P1", "-30"), txn("d", "P1", "-40")},
			want: "25",
		},
		{
			name: "income rows are not expenses",
			txns: []domain.Txn{txn("a", "P1", "-10"), txn("b", "P1", "5000"), txn("c", "P1", "-30")},
			want: "20",
		},
		{
			name: "no expenses at all",
			txns: []domain.Txn{txn("a", "P1", "100")},
			want: "0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			led := domain.NewLedger(tt.txns)
			got := medianAbsExpense(led.ByScenario["P1"])
			if !got.Equal(decimal.RequireFromString(tt.want)) {
				t.Errorf("median = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMedianIgnoresBlankAmounts(t *testing.T) {
	blank := txn("blank", "P1", "0")
	blank.AmountMissing = true
	led := domain.NewLedger([]domain.Txn{txn("a", "P1", "-10"), txn("b", "P1", "-20"), txn("c", "P1", "-30"), blank})

	if got := medianAbsExpense(led.ByScenario["P1"]); !got.Equal(decimal.RequireFromString("20")) {
		t.Errorf("median = %s, want 20", got)
	}
}

func TestBaselineVerdictsCoverEveryCell(t *testing.T) {
	tpl := &domain.Template{
		Scenarios: []string{"P1", "B4"},
		Cells: []domain.Cell{
			{ScenarioID: "P1", ClauseID: "6.1"},
			{ScenarioID: "P1", ClauseID: "6.2"},
			{ScenarioID: "B4", ClauseID: "6.3"},
		},
	}
	led := domain.NewLedger([]domain.Txn{
		txn("a", "P1", "-10"), txn("b", "P1", "-20"), txn("c", "P1", "-30"),
		txn("d", "B4", "-500.555"),
	})

	verdicts, err := BaselineVerdicts(tpl, led)
	if err != nil {
		t.Fatalf("BaselineVerdicts: %v", err)
	}
	if len(verdicts) != len(tpl.Cells) {
		t.Fatalf("verdicts = %d, want one per cell (%d)", len(verdicts), len(tpl.Cells))
	}
	for _, v := range verdicts {
		if v.Status != domain.StatusCompliant && v.Status != domain.StatusBreach {
			t.Errorf("%s/%s: status %q is not scoreable", v.ScenarioID, v.ClauseID, v.Status)
		}
		if !v.Actual.IsPositive() {
			t.Errorf("%s/%s: actual %s must be positive", v.ScenarioID, v.ClauseID, v.Actual)
		}
		if v.Actual.Exponent() < -2 {
			t.Errorf("%s/%s: actual %s has more than two decimals", v.ScenarioID, v.ClauseID, v.Actual)
		}
		if v.EvidenceID != nil {
			t.Errorf("%s/%s: the baseline must not invent an evidence id", v.ScenarioID, v.ClauseID)
		}
		if v.Source != SourceBaseline || v.Confidence != 0 {
			t.Errorf("%s/%s: a placeholder must be marked as one (source=%q confidence=%v)",
				v.ScenarioID, v.ClauseID, v.Source, v.Confidence)
		}
		if len(v.Trace) == 0 {
			t.Errorf("%s/%s: a placeholder must say so in its trace", v.ScenarioID, v.ClauseID)
		}
	}
}

func TestBaselineHandlesBorrowerWithoutExpenses(t *testing.T) {
	tpl := &domain.Template{Cells: []domain.Cell{{ScenarioID: "P9", ClauseID: "6.1"}}}
	led := domain.NewLedger([]domain.Txn{txn("a", "P9", "100")})

	verdicts, err := BaselineVerdicts(tpl, led)
	if err != nil {
		t.Fatalf("BaselineVerdicts: %v", err)
	}
	if !verdicts[0].Actual.IsPositive() {
		t.Errorf("actual = %s, want a positive placeholder", verdicts[0].Actual)
	}
}

func TestBaselineRejectsEmptyTemplate(t *testing.T) {
	if _, err := BaselineVerdicts(&domain.Template{}, domain.NewLedger(nil)); err == nil {
		t.Error("an empty template must be an error, not an empty submission")
	}
}
