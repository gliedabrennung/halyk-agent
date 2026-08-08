package ingest

import (
	"strings"
	"testing"
)

func TestScenarioIDFromTxnID(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "TXN-P1-0039", want: "P1"},
		{in: "TXN-B4-0001", want: "B4"},
		{in: "TXN-P10-0059", want: "P10"},
		{in: "TXN-9001-0036", want: "9001"},
		{in: "TXN-P1-39", want: "P1"},
		{in: "", wantErr: true},
		{in: "P1-0039", wantErr: true},
		{in: "TXN-P1", wantErr: true},
		{in: "TXN-P1-", wantErr: true},
		{in: "TXN-P1-00A9", wantErr: true},
		{in: "TXN-P-1-0039", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ScenarioIDFromTxnID(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ScenarioIDFromTxnID(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ScenarioIDFromTxnID(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ScenarioIDFromTxnID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

const _sampleCSV = `txn_id,date,account_id,counterparty,description,amount,currency
TXN-P1-0001,2025-01-15,ACC-7801,Alpha Supplies,"Machinery purchase, Almaty plant",-1250000.55,USD
TXN-P1-0002,2025-03-02,ACC-7801,Beta Holdings,Dividend distribution,-300000.00,EUR
TXN-P1-0003,2025-06-30,ACC-7801,Customer Ltd,Sales receipt,4500000.10,USD
TXN-B4-0001,2025-02-01,ACC-9100,Gamma Logistics,Freight,-15000.25,USD
`

func TestParseLedgerReader(t *testing.T) {
	led, err := ParseLedgerReader(strings.NewReader(_sampleCSV))
	if err != nil {
		t.Fatalf("ParseLedgerReader: %v", err)
	}
	if got, want := len(led.Txns), 4; got != want {
		t.Fatalf("txn count = %d, want %d", got, want)
	}

	first := led.ByID["TXN-P1-0001"]
	if first == nil {
		t.Fatal("TXN-P1-0001 not indexed")
	}
	if first.ScenarioID != "P1" {
		t.Errorf("scenario = %q, want P1", first.ScenarioID)
	}
	if first.AccountID != "ACC-7801" {
		t.Errorf("account = %q, want ACC-7801", first.AccountID)
	}

	if want := "Machinery purchase, Almaty plant"; first.Description != want {
		t.Errorf("description = %q, want %q", first.Description, want)
	}
	if got, want := first.Amount.String(), "-1250000.55"; got != want {
		t.Errorf("amount = %s, want %s", got, want)
	}
	if !first.IsExpense() {
		t.Error("negative amount should be an expense")
	}
	if got, want := first.Date.Format("2006-01-02"), "2025-01-15"; got != want {
		t.Errorf("date = %s, want %s", got, want)
	}

	if got := led.ByID["TXN-P1-0002"].Currency; got != "EUR" {
		t.Errorf("currency = %q, want EUR", got)
	}
	if led.ByID["TXN-P1-0003"].IsExpense() {
		t.Error("positive amount should not be an expense")
	}

	if got, want := len(led.ByScenario["P1"]), 3; got != want {
		t.Errorf("P1 txns = %d, want %d", got, want)
	}
	if got, want := led.AccountToScn["ACC-7801"], "P1"; got != want {
		t.Errorf("AccountToScn[ACC-7801] = %q, want %q", got, want)
	}
	if got, want := led.AccountToScn["ACC-9100"], "B4"; got != want {
		t.Errorf("AccountToScn[ACC-9100] = %q, want %q", got, want)
	}
	if got, want := strings.Join(led.ScnToAccount["P1"], ","), "ACC-7801"; got != want {
		t.Errorf("ScnToAccount[P1] = %q, want %q", got, want)
	}

	if issues := CheckBijection(led); len(issues) != 0 {
		t.Errorf("expected a clean bijection, got %v", issues)
	}
}

func TestParseLedgerReaderErrors(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want string
	}{
		{
			name: "missing column",
			csv:  "txn_id,date,account_id,counterparty,description,amount\nTXN-P1-0001,2025-01-15,ACC-1,X,Y,-1\n",
			want: "missing column \"currency\"",
		},
		{
			name: "bad txn id",
			csv:  "txn_id,date,account_id,counterparty,description,amount,currency\nWRONG-0001,2025-01-15,ACC-1,X,Y,-1,USD\n",
			want: "does not match",
		},
		{
			name: "bad date",
			csv:  "txn_id,date,account_id,counterparty,description,amount,currency\nTXN-P1-0001,15.01.2025,ACC-1,X,Y,-1,USD\n",
			want: "bad date",
		},
		{
			name: "bad amount",
			csv:  "txn_id,date,account_id,counterparty,description,amount,currency\nTXN-P1-0001,2025-01-15,ACC-1,X,Y,1 250,USD\n",
			want: "bad amount",
		},
		{
			name: "duplicate txn id",
			csv: "txn_id,date,account_id,counterparty,description,amount,currency\n" +
				"TXN-P1-0001,2025-01-15,ACC-1,X,Y,-1,USD\nTXN-P1-0001,2025-01-16,ACC-1,X,Y,-2,USD\n",
			want: "duplicate txn_id",
		},
		{
			name: "empty currency",
			csv:  "txn_id,date,account_id,counterparty,description,amount,currency\nTXN-P1-0001,2025-01-15,ACC-1,X,Y,-1,\n",
			want: "empty currency",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLedgerReader(strings.NewReader(tt.csv))
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestCheckBijectionDetectsViolations(t *testing.T) {
	csv := "txn_id,date,account_id,counterparty,description,amount,currency\n" +
		"TXN-P1-0001,2025-01-15,ACC-1,X,Y,-1,USD\n" +
		"TXN-P2-0001,2025-01-15,ACC-1,X,Y,-1,USD\n" +
		"TXN-P3-0001,2025-01-15,ACC-2,X,Y,-1,USD\n" +
		"TXN-P3-0002,2025-01-15,ACC-3,X,Y,-1,USD\n"

	led, err := ParseLedgerReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseLedgerReader: %v", err)
	}
	issues := CheckBijection(led)
	if len(issues) != 2 {
		t.Fatalf("issues = %v, want 2", issues)
	}
	if issues[0].Kind != "account_multi_scenario" || issues[0].Key != "ACC-1" {
		t.Errorf("issue[0] = %+v, want account_multi_scenario for ACC-1", issues[0])
	}
	if len(issues[0].TxnSamples) != 2 {
		t.Errorf("expected a sample txn per scenario, got %v", issues[0].TxnSamples)
	}
	if issues[1].Kind != "scenario_multi_account" || issues[1].Key != "P3" {
		t.Errorf("issue[1] = %+v, want scenario_multi_account for P3", issues[1])
	}
}

func TestParseLedgerReaderFlagsBlankAmount(t *testing.T) {
	csv := "txn_id,date,account_id,counterparty,description,amount,currency\n" +
		"TXN-P7-0033,2025-11-18,ACC-7807,State Revenue Committee,Mineral extraction tax,,USD\n" +
		"TXN-P7-0034,2025-11-19,ACC-7807,Supplier,Normal row,-100.50,USD\n"

	led, err := ParseLedgerReader(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseLedgerReader: %v", err)
	}
	blank := led.ByID["TXN-P7-0033"]
	if !blank.AmountMissing {
		t.Error("blank amount must set AmountMissing")
	}
	if !blank.Amount.IsZero() {
		t.Errorf("blank amount should park at zero, got %s", blank.Amount)
	}
	if ok := led.ByID["TXN-P7-0034"]; ok.AmountMissing {
		t.Error("a parsed amount must not be flagged as missing")
	}
}
