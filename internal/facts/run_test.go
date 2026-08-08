package facts

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func TestMissingAmountTxnsNamesOnlyThisBorrowersBlankRows(t *testing.T) {
	txns := []domain.Txn{
		{ID: "TXN-P8-0031", ScenarioID: "P8", AmountMissing: true},
		{ID: "TXN-P8-0005", ScenarioID: "P8"},
		{ID: "TXN-P7-0033", ScenarioID: "P7", AmountMissing: true},
		{ID: "TXN-P8-0002", ScenarioID: "P8", AmountMissing: true},
	}

	got := missingAmountTxns(txns, "P8")
	want := []string{"TXN-P8-0002", "TXN-P8-0031"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted, this borrower only)", got, want)
		}
	}
}

func TestMissingAmountTxnsIsEmptyWhenEveryRowHasAnAmount(t *testing.T) {
	txns := []domain.Txn{
		{ID: "TXN-P1-0001", ScenarioID: "P1"},
		{ID: "TXN-P1-0002", ScenarioID: "P1"},
	}
	if got := missingAmountTxns(txns, "P1"); len(got) != 0 {
		t.Errorf("got %v, want nothing to recover", got)
	}
}

func TestUnfixedAmountsNamesWhatNoDocumentStates(t *testing.T) {
	fb := &domain.FactBase{Adjustments: []domain.Adjustment{
		{Kind: domain.AdjLedgerAmountFix, TxnID: "TXN-P1-0031", Amount: decimal.RequireFromString("884204.16"), Applied: true},
		{Kind: domain.AdjLedgerAmountFix, TxnID: "TXN-P1-0044", Amount: decimal.RequireFromString("100"), Applied: false},
		{Kind: domain.AdjReclassify, TxnID: "TXN-P1-0033", Amount: decimal.RequireFromString("500"), Applied: true},
	}}
	got := unfixedAmounts([]string{"TXN-P1-0031", "TXN-P1-0033", "TXN-P1-0044"}, fb)
	want := []string{"TXN-P1-0033", "TXN-P1-0044"}
	if len(got) != len(want) {
		t.Fatalf("unfixed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unfixed = %v, want %v", got, want)
		}
	}
	if len(unfixedAmounts(nil, fb)) != 0 {
		t.Error("nothing requested, nothing to report")
	}
}
