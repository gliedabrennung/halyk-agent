package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
)

const TriageSchemaVersion = "triage-v1"

const _triageRepairPrompt = "Your previous answer could not be parsed: %v\n\nYour answer was:\n%s\n\n" +
	"Return the same information as STRICT JSON matching the schema, nothing else."

type TriageResult struct {
	DocType        domain.DocType `json:"doc_type"`
	CompanyName    string         `json:"company_name"`
	AccountIDs     []string       `json:"account_ids"`
	Period         string         `json:"period"`
	EffectiveDate  string         `json:"effective_date"`
	IsSuperseded   bool           `json:"is_superseded"`
	Amends         string         `json:"amends_document"`
	Counterparties []string       `json:"counterparties_mentioned"`
	Language       string         `json:"language"`
	Summary        string         `json:"summary"`
	Confidence     float64        `json:"confidence"`
}

type TriageInput struct {
	DocID string
	Pages int

	Text string

	Findings string

	OCRText string
}

func Triage(ctx context.Context, client *llm.Client, in TriageInput) (*TriageResult, error) {
	req := llm.Request{
		Name:          "doc_triage",
		Description:   "Classifies an opaque archive document and identifies its subject company.",
		Instruction:   _triageInstruction,
		Prompt:        in.prompt(),
		SchemaVersion: TriageSchemaVersion,
		JSON:          true,
	}

	raw, err := client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := parseTriage(raw)
	if err == nil {
		return res, nil
	}

	repair := req
	repair.SchemaVersion = TriageSchemaVersion + "-repair"
	repair.Prompt = fmt.Sprintf(_triageRepairPrompt, err, raw)
	raw2, err2 := client.Complete(ctx, repair)
	if err2 != nil {
		return nil, fmt.Errorf("triage %s: %w (repair call also failed: %v)", in.DocID, err, err2)
	}
	res, err2 = parseTriage(raw2)
	if err2 != nil {
		return nil, fmt.Errorf("triage %s: unparseable after repair: %w", in.DocID, err2)
	}
	return res, nil
}

func parseTriage(raw string) (*TriageResult, error) {
	var res TriageResult
	if err := json.Unmarshal([]byte(stripFence(raw)), &res); err != nil {
		return nil, fmt.Errorf("decode triage json: %w", err)
	}
	if !validDocType(res.DocType) {
		return nil, fmt.Errorf("doc_type %q is not one of the allowed values", res.DocType)
	}
	if res.Confidence < 0 || res.Confidence > 1 {
		return nil, fmt.Errorf("confidence %v is outside 0..1", res.Confidence)
	}
	return &res, nil
}

func validDocType(t domain.DocType) bool {
	switch t {
	case domain.DocCreditAgreement, domain.DocAmendment, domain.DocAuditReport,
		domain.DocKYCDossier, domain.DocCorporateStructure, domain.DocFXTable, domain.DocOther:
		return true
	}
	return false
}
