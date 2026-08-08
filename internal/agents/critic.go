package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
)

type CriticVerdict struct {
	OK        bool            `json:"ok"`
	Note      string          `json:"note"`
	Corrected json.RawMessage `json:"corrected"`
}

func criticise(
	ctx context.Context,
	client *llm.Client,
	model string,
	in CovenantInput,
	spec *domain.CovenantSpec,
) (*CriticVerdict, error) {
	specJSON, err := json.MarshalIndent(specWire(spec), "", "  ")
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Borrower: %s (scenario %s)\nClause: %s\n", in.Company, in.ScenarioID, in.ClauseID)
	fmt.Fprintf(&b, "\n--- the clause, verbatim ---\n%s\n", in.ClauseText)
	if in.AmendmentsIn != "" {
		fmt.Fprintf(&b, "\n--- amendment governing this clause ---\n%s\n", in.AmendmentsIn)
	}
	if in.Definitions != "" {
		fmt.Fprintf(&b, "\n--- defined terms ---\n%s\n", in.Definitions)
	}
	fmt.Fprintf(&b, "\n--- the specification to audit ---\n%s\n", specJSON)

	raw, err := client.Complete(ctx, llm.Request{
		Name:          "covenant_critic",
		Model:         model,
		Description:   "Validates a covenant specification against the contract text.",
		Instruction:   _criticInstruction,
		Prompt:        b.String(),
		SchemaVersion: CriticSchemaVersion,
		JSON:          true,
	})
	if err != nil {
		return nil, err
	}

	var v CriticVerdict
	if err := json.Unmarshal([]byte(stripFence(raw)), &v); err != nil {

		return &CriticVerdict{
			OK:   true,
			Note: "critic output was unparseable (" + err.Error() + "); specification left unchanged",
		}, nil
	}
	return &v, nil
}

func specWire(spec *domain.CovenantSpec) covenantJSON {
	out := covenantJSON{
		ClauseID:     spec.ClauseID,
		Title:        spec.Title,
		Expression:   spec.Expression,
		Op:           spec.Op,
		Threshold:    spec.Threshold.String(),
		Unit:         spec.Unit,
		EvidenceKind: spec.EvidenceKind,
		Quote:        spec.SourceRef.Quote,
		Confidence:   spec.Confidence,
	}
	for _, t := range spec.Terms {
		wire := termJSON{
			Name: t.Name, Kind: string(t.Kind), Line: t.Line, Description: t.Description,
			Reclassification: t.Reclassification, EntitySource: t.EntitySource,
			EntityScope: t.EntityScope, Direction: t.Direction,
		}
		if !t.Constant.IsZero() {
			wire.Constant = t.Constant.String()
		}
		out.Terms = append(out.Terms, wire)
	}
	out.Period.Kind = spec.Period.Kind
	out.Period.Label = spec.Period.Label
	if !spec.Period.From.IsZero() {
		out.Period.From = spec.Period.From.Format("2006-01-02")
	}
	if !spec.Period.To.IsZero() {
		out.Period.To = spec.Period.To.Format("2006-01-02")
	}
	if spec.Trigger != nil {
		out.Trigger = &struct {
			Expression  string `json:"expression"`
			Description string `json:"description"`
			SourceQuote string `json:"source_quote"`
		}{spec.Trigger.Expression, spec.Trigger.Description, spec.Trigger.SourceQuote}
	}
	for _, c := range spec.Carveouts {
		item := struct {
			Condition struct {
				Expression  string `json:"expression"`
				Description string `json:"description"`
				SourceQuote string `json:"source_quote"`
			} `json:"condition"`
			Description string `json:"description"`
			Cap         string `json:"cap"`
		}{Description: c.Description}
		item.Condition.Expression = c.Condition.Expression
		item.Condition.Description = c.Condition.Description
		item.Condition.SourceQuote = c.Condition.SourceQuote
		if !c.Cap.IsZero() {
			item.Cap = c.Cap.String()
		}
		out.Carveouts = append(out.Carveouts, item)
	}
	return out
}
