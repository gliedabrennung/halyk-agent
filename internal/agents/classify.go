package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
)

const _classifyRepairPrompt = "Your previous answer was rejected: %v\n\nIt was:\n%s\n\n" +
	"Return corrected STRICT JSON for the same items.\n\n%s"

const ClassifySchemaVersion = "classify-v1"

type ClassifyItem struct {
	Pattern string

	Count int

	Outflows int
	Inflows  int
	Unpriced int

	Samples        []string
	Counterparties []string
}

type ClassifyResult struct {
	Category   domain.Category
	Contra     bool
	Confidence float64
	Rationale  string
}

type classifyJSON struct {
	Labels []struct {
		Index      *int    `json:"i"`
		Category   string  `json:"category"`
		Contra     *bool   `json:"contra"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	} `json:"labels"`
}

func ClassifyPatterns(
	ctx context.Context,
	client *llm.Client,
	model string,
	items []ClassifyItem,
) ([]ClassifyResult, error) {
	req := llm.Request{
		Name:          "txn_classifier",
		Model:         model,
		Description:   "Assigns ledger description patterns to the covenant taxonomy.",
		Instruction:   classifyInstruction(),
		Prompt:        classifyPrompt(items),
		SchemaVersion: ClassifySchemaVersion,
		JSON:          true,
	}
	raw, err := client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	out, err := parseClassify(raw, len(items))
	if err == nil {
		return out, nil
	}

	repair := req
	repair.SchemaVersion = ClassifySchemaVersion + "-repair"
	repair.Prompt = fmt.Sprintf(_classifyRepairPrompt,
		err, raw, req.Prompt)
	raw2, err2 := client.Complete(ctx, repair)
	if err2 != nil {
		return nil, fmt.Errorf("classify batch: %w", err)
	}
	out, err2 = parseClassify(raw2, len(items))
	if err2 != nil {
		return nil, fmt.Errorf("classify batch: unusable after repair: %w", err2)
	}
	return out, nil
}

func parseClassify(raw string, n int) ([]ClassifyResult, error) {
	var cj classifyJSON
	if err := json.Unmarshal([]byte(stripFence(raw)), &cj); err != nil {
		return nil, fmt.Errorf("decode classify json: %w", err)
	}
	out := make([]ClassifyResult, n)
	seen := make([]bool, n)
	for _, l := range cj.Labels {
		if l.Index == nil || *l.Index < 0 || *l.Index >= n {
			return nil, fmt.Errorf("label index %v is outside 0..%d", l.Index, n-1)
		}
		cat := domain.Category(strings.TrimSpace(strings.ToLower(l.Category)))
		if !domain.ValidCategory(cat) {
			return nil, fmt.Errorf("category %q is not in the taxonomy", l.Category)
		}
		res := ClassifyResult{
			Category:   cat,
			Confidence: l.Confidence,
			Rationale:  strings.TrimSpace(l.Rationale),
		}
		if l.Contra != nil {
			res.Contra = *l.Contra
		}
		out[*l.Index] = res
		seen[*l.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("no label returned for item %d", i)
		}
	}
	return out, nil
}

type Dispute struct {
	Item     ClassifyItem
	RuleCat  domain.Category
	RuleCtr  bool
	ModelCat domain.Category
	ModelCtr bool

	Reason string
}

func ResolveDisputes(
	ctx context.Context,
	client *llm.Client,
	model string,
	disputes []Dispute,
) ([]ClassifyResult, error) {
	req := llm.Request{
		Name:          "txn_classifier_escalation",
		Model:         model,
		Description:   "Settles disagreements between the keyword rules and the fast classifier.",
		Instruction:   _resolveInstruction,
		Prompt:        resolvePrompt(disputes),
		SchemaVersion: ClassifySchemaVersion + "-escalate",
		JSON:          true,
	}
	raw, err := client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	out, err := parseClassify(raw, len(disputes))
	if err != nil {
		return nil, fmt.Errorf("escalation: %w", err)
	}
	return out, nil
}
