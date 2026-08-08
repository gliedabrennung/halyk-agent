package facts

import (
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

// Rows the export left without an amount are named to the model so it can look
// the figure up in the documents. Missing one reports the row as zero, which
// silently understates every total it belongs to.
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
