package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type Txn struct {
	ID           string          `json:"txn_id"`
	ScenarioID   string          `json:"scenario_id"`
	AccountID    string          `json:"account_id"`
	Date         time.Time       `json:"date"`
	Counterparty string          `json:"counterparty"`
	Description  string          `json:"description"`
	Amount       decimal.Decimal `json:"amount"`
	Currency     string          `json:"currency"`
	AmountUSD    decimal.Decimal `json:"amount_usd"`

	AmountMissing bool `json:"amount_missing,omitempty"`
}

func (t Txn) IsExpense() bool { return t.Amount.IsNegative() }

type Ledger struct {
	Txns []Txn

	ByID         map[string]*Txn
	ByScenario   map[string][]*Txn
	AccountToScn map[string]string
	ScnToAccount map[string][]string
}

func NewLedger(txns []Txn) *Ledger {
	led := &Ledger{
		Txns:         txns,
		ByID:         make(map[string]*Txn, len(txns)),
		ByScenario:   map[string][]*Txn{},
		AccountToScn: map[string]string{},
		ScnToAccount: map[string][]string{},
	}
	seen := map[string]map[string]bool{}
	for i := range led.Txns {
		t := &led.Txns[i]
		led.ByID[t.ID] = t
		led.ByScenario[t.ScenarioID] = append(led.ByScenario[t.ScenarioID], t)
		if seen[t.AccountID] == nil {
			seen[t.AccountID] = map[string]bool{}
		}
		seen[t.AccountID][t.ScenarioID] = true
	}
	for acc, scns := range seen {
		names := make([]string, 0, len(scns))
		for s := range scns {
			names = append(names, s)
		}
		sort.Strings(names)
		led.AccountToScn[acc] = names[0]
		for _, s := range names {
			led.ScnToAccount[s] = append(led.ScnToAccount[s], acc)
		}
	}
	for s := range led.ScnToAccount {
		sort.Strings(led.ScnToAccount[s])
	}
	return led
}
