package domain

import (
	"github.com/shopspring/decimal"
)

type Label struct {
	Pattern  string   `json:"pattern"`
	Category Category `json:"category"`

	Contra bool `json:"contra"`

	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale,omitempty"`

	RuleCategory Category `json:"rule_category,omitempty"`
	RuleID       string   `json:"rule_id,omitempty"`
	Disputed     bool     `json:"disputed,omitempty"`

	Count   int      `json:"count"`
	Samples []string `json:"samples,omitempty"`
}

type TxnLabel struct {
	TxnID    string   `json:"txn_id"`
	Pattern  string   `json:"pattern"`
	Category Category `json:"category"`
	Contra   bool     `json:"contra"`
	Source   string   `json:"source"`

	Confidence float64 `json:"confidence"`

	Counterparty string `json:"counterparty"`

	RelatedParty bool            `json:"related_party"`
	PartyName    string          `json:"party_name,omitempty"`
	VotingShare  decimal.Decimal `json:"voting_share,omitempty"`

	Reclassified   bool     `json:"reclassified,omitempty"`
	ReclassifiedTo Category `json:"reclassified_to,omitempty"`
	AdjustmentKind string   `json:"adjustment_kind,omitempty"`
}

type LabelSet struct {
	ScenarioID string     `json:"scenario_id"`
	Company    string     `json:"company,omitempty"`
	Txns       []TxnLabel `json:"txns"`

	RelatedParties   []string `json:"related_parties,omitempty"`
	UnmatchedParties []string `json:"unmatched_parties,omitempty"`

	Totals map[Category]decimal.Decimal `json:"totals,omitempty"`
}

func (l *LabelSet) ByCategory(c Category) []TxnLabel {
	var out []TxnLabel
	for _, t := range l.Txns {
		if t.Category == c {
			out = append(out, t)
		}
	}
	return out
}

func (l *LabelSet) Lookup(txnID string) (TxnLabel, bool) {
	for _, t := range l.Txns {
		if t.TxnID == txnID {
			return t, true
		}
	}
	return TxnLabel{}, false
}
