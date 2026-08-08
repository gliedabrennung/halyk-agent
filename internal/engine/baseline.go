package engine

import (
	"fmt"
	"slices"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

const (
	SourceEngine   = "engine"
	SourceBaseline = "baseline"
)

const BaselineStatus = domain.StatusCompliant

func BaselineVerdicts(tpl *domain.Template, led *domain.Ledger) ([]domain.Verdict, error) {
	if tpl == nil || len(tpl.Cells) == 0 {
		return nil, fmt.Errorf("template has no cells")
	}
	if led == nil {
		return nil, fmt.Errorf("ledger is nil")
	}

	medians := make(map[string]decimal.Decimal, len(led.ByScenario))
	for scn, txns := range led.ByScenario {
		medians[scn] = medianAbsExpense(txns)
	}

	out := make([]domain.Verdict, 0, len(tpl.Cells))
	for _, c := range tpl.Cells {
		actual, ok := medians[c.ScenarioID]
		if !ok || actual.IsZero() {

			actual = decimal.NewFromInt(1)
		}
		out = append(out, domain.Verdict{
			ScenarioID: c.ScenarioID,
			ClauseID:   c.ClauseID,
			Status:     BaselineStatus,
			Actual:     actual.Round(2),
			EvidenceID: nil,
			Source:     SourceBaseline,
			Confidence: 0,
			Trace: []string{
				"baseline placeholder: no CovenantSpec extracted yet",
				"actual = median absolute expense of the borrower (magnitude only, not a computed metric)",
				"status = " + BaselineStatus + " by default; an empty cell scores the same as a wrong one",
			},
		})
	}
	return out, nil
}

func medianAbsExpense(txns []*domain.Txn) decimal.Decimal {
	var amounts []decimal.Decimal
	for _, t := range txns {
		if t.AmountMissing || !t.Amount.IsNegative() {
			continue
		}
		amounts = append(amounts, t.Amount.Abs())
	}
	if len(amounts) == 0 {
		return decimal.Zero
	}
	slices.SortFunc(amounts, decimal.Decimal.Cmp)

	mid := len(amounts) / 2
	if len(amounts)%2 == 1 {
		return amounts[mid]
	}
	return amounts[mid-1].Add(amounts[mid]).Div(decimal.NewFromInt(2))
}
