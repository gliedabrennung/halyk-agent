package agents

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/llm"
)

const VerdictCriticSchemaVersion = "verdict-critic-v2"

type VerdictCriticInput struct {
	ScenarioID string
	ClauseID   string
	Company    string
	ClauseText string

	Metric string
	Period string

	Terms []string

	Rows []string

	Disclosures []string

	Parties []string

	Status   string
	Actual   string
	Evidence string
}

type VerdictReview struct {
	Agrees     bool    `json:"agrees"`
	Concern    string  `json:"concern"`
	Issue      string  `json:"issue"`
	Confidence float64 `json:"confidence"`
}

func ReviewVerdict(
	ctx context.Context,
	client *llm.Client,
	model string,
	in VerdictCriticInput,
) (*VerdictReview, error) {
	raw, err := client.Complete(ctx, llm.Request{
		Name:          "verdict_critic",
		Model:         model,
		Description:   "Reviews one computed covenant cell against its clause.",
		Instruction:   _verdictCriticInstruction,
		Prompt:        in.prompt(),
		SchemaVersion: VerdictCriticSchemaVersion,
		JSON:          true,
	})
	if err != nil {
		return nil, err
	}

	var v VerdictReview
	if err := json.Unmarshal([]byte(stripFence(raw)), &v); err != nil {

		return &VerdictReview{
			Agrees:  false,
			Concern: "the critic's answer could not be read (" + err.Error() + ")",
			Issue:   "other",
		}, nil
	}
	v.Issue = strings.TrimSpace(strings.ToLower(v.Issue))
	if v.Agrees && (v.Issue == "" || v.Issue == "none") {
		v.Concern = ""
	}
	return &v, nil
}
