package domain

import (
	"github.com/shopspring/decimal"
)

const (
	AdjReclassify      = "reclassify"
	AdjExcludePeriod   = "exclude_period"
	AdjIncludePeriod   = "include_period"
	AdjDisclosedAmount = "disclosed_amount"
	AdjLedgerAmountFix = "ledger_amount_fix"
	AdjEBITDAAddBack   = "ebitda_add_back"
	AdjNoChange        = "no_change"
)

type Adjustment struct {
	Kind         string          `json:"kind"`
	TxnID        string          `json:"txn_id,omitempty"`
	Counterparty string          `json:"counterparty,omitempty"`
	Amount       decimal.Decimal `json:"amount"`
	FromCategory string          `json:"from_category,omitempty"`
	ToCategory   string          `json:"to_category,omitempty"`
	Rationale    string          `json:"rationale,omitempty"`

	Applied   bool    `json:"applied"`
	SourceRef PageRef `json:"source_ref"`
}

type Party struct {
	Name string `json:"name"`

	VotingShare decimal.Decimal `json:"voting_share"`

	Related  bool   `json:"related"`
	Relation string `json:"relation,omitempty"`

	PledgedShare decimal.Decimal `json:"pledged_share,omitempty"`

	Status    string  `json:"status,omitempty"`
	SourceRef PageRef `json:"source_ref"`
}

const (
	StatusRestricted   = "restricted"
	StatusUnrestricted = "unrestricted"
)

type GroupPPE struct {
	Parent  string          `json:"parent"`
	Period  string          `json:"period"`
	Opening decimal.Decimal `json:"opening"`
	Closing decimal.Decimal `json:"closing"`

	Depreciation decimal.Decimal `json:"depreciation"`
	Disposals    decimal.Decimal `json:"disposals"`

	DisposalsStated bool    `json:"disposals_stated"`
	SourceRef       PageRef `json:"source_ref"`
}

func (g *GroupPPE) Capex() (decimal.Decimal, bool) {
	if g == nil || !g.DisposalsStated {
		return decimal.Zero, false
	}
	if g.Opening.IsZero() || g.Closing.IsZero() {
		return decimal.Zero, false
	}
	capex := g.Closing.Sub(g.Opening).Add(g.Depreciation).Add(g.Disposals.Abs())
	if !capex.IsPositive() {
		return decimal.Zero, false
	}
	return capex, true
}

type FactBase struct {
	ScenarioID string `json:"scenario_id"`
	Company    string `json:"company"`

	Adjustments []Adjustment `json:"adjustments"`
	Parties     []Party      `json:"parties"`

	GroupPPE *GroupPPE `json:"group_ppe,omitempty"`

	RelatedPartyThreshold decimal.Decimal `json:"related_party_threshold"`

	UnrestrictedThreshold decimal.Decimal `json:"unrestricted_threshold,omitempty"`

	FXRates map[string]decimal.Decimal `json:"fx_rates,omitempty"`

	SourceDocs []string `json:"source_docs"`
	Notes      []string `json:"notes,omitempty"`
	Confidence float64  `json:"confidence"`
}

func (f *FactBase) RelatedNames() []string {
	var out []string
	for _, p := range f.Parties {
		if p.Related {
			out = append(out, p.Name)
		}
	}
	return out
}

func (f *FactBase) UnrestrictedNames() []string {
	var out []string
	for _, p := range f.Parties {
		if p.Status == StatusUnrestricted {
			out = append(out, p.Name)
		}
	}
	return out
}

func (f *FactBase) AppliedAdjustments() []Adjustment {
	var out []Adjustment
	for _, a := range f.Adjustments {
		if a.Applied {
			out = append(out, a)
		}
	}
	return out
}
