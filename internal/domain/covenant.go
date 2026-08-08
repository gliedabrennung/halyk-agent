package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

type Period struct {
	Kind  string    `json:"kind"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Label string    `json:"label,omitempty"`
}

type Scope struct {
	Categories          []string `json:"categories,omitempty"`
	ExcludeCategories   []string `json:"exclude_categories,omitempty"`
	Counterparties      []string `json:"counterparties,omitempty"`
	EntitySets          []string `json:"entity_sets,omitempty"`
	IncludeSubsidiaries bool     `json:"include_subsidiaries"`
	Direction           string   `json:"direction,omitempty"`
}

type Condition struct {
	Expression  string `json:"expression"`
	Description string `json:"description"`
	SourceQuote string `json:"source_quote,omitempty"`
}

const (
	UnitUSD   = "USD"
	UnitRatio = "ratio"
)

type Carveout struct {
	Condition   Condition `json:"condition"`
	Description string    `json:"description"`

	Cap decimal.Decimal `json:"cap,omitempty"`

	SourceRef PageRef `json:"source_ref,omitempty"`
}

type Definition struct {
	Term      string  `json:"term"`
	Text      string  `json:"text"`
	SourceRef PageRef `json:"source_ref,omitempty"`
}

type TermKind string

const (
	TermStatementLine TermKind = "statement_line"

	TermStatementNote TermKind = "statement_note"

	TermLedgerCategory TermKind = "ledger_category"

	TermRelatedPartyPayments TermKind = "related_party_payments"

	TermGroupConsolidated TermKind = "group_consolidated"

	TermConstant TermKind = "constant"
)

const (
	ReclassInclude = "include_in"

	ReclassExclude = "exclude_from"

	ReclassBoth = "both"

	ReclassIgnore = "ignore"
)

type Term struct {
	Name        string   `json:"name"`
	Kind        TermKind `json:"kind"`
	Line        string   `json:"line,omitempty"`
	Description string   `json:"description,omitempty"`

	Reclassification string `json:"reclassification,omitempty"`

	EntitySource string `json:"entity_source,omitempty"`

	// EntityScope сужает терм до контрагентов с этим статусом в факт-базе:
	// StatusRestricted, StatusUnrestricted или пусто — все контрагенты.
	// Объявляется спецификацией; движок его не выводит из текста пункта.
	EntityScope string `json:"entity_scope,omitempty"`

	Direction string          `json:"direction,omitempty"`
	Constant  decimal.Decimal `json:"constant,omitempty"`
}

// ValidEntityScope сообщает, что скоуп — известный статус стороны или пусто.
func ValidEntityScope(s string) bool {
	return s == "" || s == StatusRestricted || s == StatusUnrestricted
}

type CovenantSpec struct {
	ScenarioID string `json:"scenario_id"`
	ClauseID   string `json:"clause_id"`
	Title      string `json:"title"`

	Expression string `json:"expression"`
	Terms      []Term `json:"terms"`

	Op        string          `json:"op"`
	Threshold decimal.Decimal `json:"threshold"`
	Unit      string          `json:"unit"`

	Period    Period     `json:"period"`
	Trigger   *Condition `json:"trigger,omitempty"`
	Carveouts []Carveout `json:"carveouts,omitempty"`

	EvidenceKind string       `json:"evidence_kind"`
	Definitions  []Definition `json:"definitions,omitempty"`
	SourceRef    PageRef      `json:"source_ref"`
	Confidence   float64      `json:"confidence"`

	CriticPasses int      `json:"critic_passes"`
	CriticNotes  []string `json:"critic_notes,omitempty"`
}

func (c *CovenantSpec) TermByName(name string) (Term, bool) {
	for _, t := range c.Terms {
		if t.Name == name {
			return t, true
		}
	}
	return Term{}, false
}
