package domain

import (
	"github.com/shopspring/decimal"
)

type Verdict struct {
	ScenarioID      string          `json:"scenario_id"`
	ClauseID        string          `json:"clause_id"`
	Status          string          `json:"status"`
	Actual          decimal.Decimal `json:"actual"`
	EvidenceID      *string         `json:"evidence_txn_id"`
	Contributors    []string        `json:"contributors,omitempty"`
	CarveoutApplied string          `json:"carveout_applied,omitempty"`
	Source          string          `json:"source"`

	TriggerFired bool `json:"trigger_fired"`

	MetricUndefined bool     `json:"metric_undefined,omitempty"`
	Confidence      float64  `json:"confidence"`
	Trace           []string `json:"trace,omitempty"`
}

const (
	StatusCompliant = "COMPLIANT"
	StatusBreach    = "BREACH"
)
