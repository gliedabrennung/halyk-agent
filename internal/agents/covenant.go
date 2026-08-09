package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
)

const CovenantSchemaVersion = "covenant-v1"

const CriticSchemaVersion = "covenant-critic-v1"

type CovenantInput struct {
	ScenarioID   string
	ClauseID     string
	Company      string
	ClauseText   string
	ArticleText  string
	Definitions  string
	AmendmentsIn string
}

type termJSON struct {
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Line             string `json:"line"`
	Description      string `json:"description"`
	Reclassification string `json:"reclassification"`
	EntitySource     string `json:"entity_source"`
	EntityScope      string `json:"entity_scope"`
	Category         string `json:"category"`
	Direction        string `json:"direction"`
	Constant         string `json:"constant"`
}

type covenantJSON struct {
	ClauseID   string     `json:"clause_id"`
	Title      string     `json:"title"`
	Expression string     `json:"expression"`
	Terms      []termJSON `json:"terms"`
	Op         string     `json:"op"`
	Threshold  string     `json:"threshold"`
	Unit       string     `json:"unit"`
	Period     struct {
		Kind  string `json:"kind"`
		From  string `json:"from"`
		To    string `json:"to"`
		Label string `json:"label"`
	} `json:"period"`
	Trigger *struct {
		Expression  string `json:"expression"`
		Description string `json:"description"`
		SourceQuote string `json:"source_quote"`
	} `json:"trigger"`
	Carveouts []struct {
		Condition struct {
			Expression  string `json:"expression"`
			Description string `json:"description"`
			SourceQuote string `json:"source_quote"`
		} `json:"condition"`
		Description string `json:"description"`
		Cap         string `json:"cap"`
	} `json:"carveouts"`
	EvidenceKind string  `json:"evidence_kind"`
	Quote        string  `json:"quote"`
	Confidence   float64 `json:"confidence"`
}

const _covenantRepairPrompt = "Your previous answer was rejected: %v\n\nIt was:\n%s\n\n" +
	"Return a corrected STRICT JSON specification for the same clause.\n\n%s"

func ExtractCovenant(
	ctx context.Context,
	client *llm.Client,
	model string,
	in CovenantInput,
	maxCriticPasses int,
) (*domain.CovenantSpec, error) {
	spec, err := completeWithRepair(ctx, client, llm.Request{
		Name:          "covenant_extract",
		Model:         model,
		Description:   "Turns one covenant clause into an executable specification.",
		Instruction:   covenantInstruction(),
		Prompt:        in.prompt(),
		SchemaVersion: CovenantSchemaVersion,
		JSON:          true,
	}, in.ScenarioID+"/"+in.ClauseID,
		func(cause error, raw string) string {
			return fmt.Sprintf(_covenantRepairPrompt, cause, raw, in.prompt())
		},
		func(raw string) (*domain.CovenantSpec, error) { return parseCovenant(raw, in) })
	if err != nil {
		return nil, err
	}

	for pass := 1; pass <= maxCriticPasses; pass++ {
		verdict, err := criticise(ctx, client, model, in, spec)
		if err != nil {
			return nil, err
		}
		spec.CriticPasses = pass
		if verdict.OK {
			if verdict.Note != "" {
				spec.CriticNotes = append(spec.CriticNotes, "pass "+fmt.Sprint(pass)+": accepted — "+verdict.Note)
			}
			break
		}
		spec.CriticNotes = append(spec.CriticNotes, fmt.Sprintf("pass %d: %s", pass, verdict.Note))
		if verdict.Corrected == nil {
			break
		}
		corrected, err := parseCovenantJSON(verdict.Corrected, in)
		if err != nil {
			spec.CriticNotes = append(spec.CriticNotes,
				fmt.Sprintf("pass %d: the correction was unusable (%v); keeping the previous specification", pass, err))
			break
		}
		corrected.CriticNotes = spec.CriticNotes
		corrected.CriticPasses = pass
		spec = corrected
	}
	return spec, nil
}

func parseCovenant(raw string, in CovenantInput) (*domain.CovenantSpec, error) {
	cleaned := stripFence(raw)
	var cj covenantJSON
	if err := json.Unmarshal([]byte(cleaned), &cj); err != nil {
		return nil, fmt.Errorf("decode covenant json: %w", err)
	}
	return buildSpec(&cj, in)
}

func parseCovenantJSON(raw json.RawMessage, in CovenantInput) (*domain.CovenantSpec, error) {
	var cj covenantJSON
	if err := json.Unmarshal(raw, &cj); err != nil {
		return nil, fmt.Errorf("decode corrected covenant json: %w", err)
	}
	return buildSpec(&cj, in)
}

func buildSpec(cj *covenantJSON, in CovenantInput) (*domain.CovenantSpec, error) {
	spec := &domain.CovenantSpec{
		ScenarioID:   in.ScenarioID,
		ClauseID:     in.ClauseID,
		Title:        strings.TrimSpace(cj.Title),
		Expression:   strings.TrimSpace(cj.Expression),
		Op:           strings.TrimSpace(cj.Op),
		Unit:         strings.TrimSpace(cj.Unit),
		EvidenceKind: strings.TrimSpace(cj.EvidenceKind),
		Confidence:   cj.Confidence,
	}
	if spec.Expression == "" {
		return nil, errors.New("expression is empty")
	}
	switch spec.Op {
	case "<=", ">=", "<", ">":
	default:
		return nil, fmt.Errorf("op %q is not one of <=, >=, <, >", spec.Op)
	}
	switch spec.EvidenceKind {
	case "single_txn", "aggregate", "ratio":
	default:
		return nil, fmt.Errorf("evidence_kind %q is not one of single_txn, aggregate, ratio", spec.EvidenceKind)
	}
	switch spec.Unit {
	case domain.UnitUSD, domain.UnitRatio:
	default:
		return nil, fmt.Errorf("unit %q is not one of %s, %s", spec.Unit, domain.UnitUSD, domain.UnitRatio)
	}

	th, err := parseDecimal(cj.Threshold)
	if err != nil {
		return nil, fmt.Errorf("threshold %q: %w", cj.Threshold, err)
	}
	spec.Threshold = th

	for _, t := range cj.Terms {
		term := domain.Term{
			Name:             strings.TrimSpace(t.Name),
			Kind:             domain.TermKind(strings.TrimSpace(t.Kind)),
			Line:             strings.TrimSpace(t.Line),
			Description:      strings.TrimSpace(t.Description),
			Reclassification: strings.TrimSpace(t.Reclassification),
			EntitySource:     strings.TrimSpace(t.EntitySource),
			Direction:        strings.TrimSpace(t.Direction),
		}
		if term.Name == "" {
			return nil, errors.New("a term has no name")
		}
		if !validTermKind(term.Kind) {
			return nil, fmt.Errorf("term %q has kind %q, which is not allowed", term.Name, term.Kind)
		}
		if term.Reclassification, err = normaliseReclassification(term.Reclassification); err != nil {
			return nil, fmt.Errorf("term %q: %w", term.Name, err)
		}
		term.EntitySource = normaliseEntitySource(term.Kind, term.EntitySource)
		if term.EntityScope, err = normaliseEntityScope(t.EntityScope); err != nil {
			return nil, fmt.Errorf("term %q: %w", term.Name, err)
		}
		if term.Category, err = normaliseTermCategory(t.Category); err != nil {
			return nil, fmt.Errorf("term %q: %w", term.Name, err)
		}
		if t.Constant != "" {
			c, err := parseDecimal(t.Constant)
			if err != nil {
				return nil, fmt.Errorf("term %q constant %q: %w", term.Name, t.Constant, err)
			}
			term.Constant = c
		}
		spec.Terms = append(spec.Terms, term)
	}

	spec.Period = domain.Period{Kind: strings.TrimSpace(cj.Period.Kind), Label: strings.TrimSpace(cj.Period.Label)}
	if spec.Period.From, err = parseDate(cj.Period.From); err != nil {
		return nil, fmt.Errorf("period.from %q: %w", cj.Period.From, err)
	}
	if spec.Period.To, err = parseDate(cj.Period.To); err != nil {
		return nil, fmt.Errorf("period.to %q: %w", cj.Period.To, err)
	}

	if spec.Period.From.IsZero() || spec.Period.To.IsZero() {
		return nil, fmt.Errorf("period is incomplete (from=%q to=%q); both dates are required",
			cj.Period.From, cj.Period.To)
	}
	if !spec.Period.To.IsZero() && spec.Period.To.Before(spec.Period.From) {
		return nil, fmt.Errorf("period ends (%s) before it starts (%s)",
			cj.Period.To, cj.Period.From)
	}

	if cj.Trigger != nil && strings.TrimSpace(cj.Trigger.Expression) != "" {
		spec.Trigger = &domain.Condition{
			Expression:  strings.TrimSpace(cj.Trigger.Expression),
			Description: strings.TrimSpace(cj.Trigger.Description),
			SourceQuote: strings.TrimSpace(cj.Trigger.SourceQuote),
		}
	}
	for _, c := range cj.Carveouts {
		if strings.TrimSpace(c.Description) == "" && strings.TrimSpace(c.Condition.Expression) == "" {
			continue
		}
		co := domain.Carveout{
			Condition: domain.Condition{
				Expression:  strings.TrimSpace(c.Condition.Expression),
				Description: strings.TrimSpace(c.Condition.Description),
				SourceQuote: strings.TrimSpace(c.Condition.SourceQuote),
			},
			Description: strings.TrimSpace(c.Description),
		}
		if c.Cap != "" {
			limit, err := parseDecimal(c.Cap)
			if err != nil {
				return nil, fmt.Errorf("carveout cap %q: %w", c.Cap, err)
			}
			co.Cap = limit
		}
		spec.Carveouts = append(spec.Carveouts, co)
	}

	if err := checkExpressionTerms(spec); err != nil {
		return nil, err
	}

	spec.SourceRef = domain.PageRef{Quote: strings.TrimSpace(cj.Quote)}
	return spec, nil
}

func normaliseReclassification(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", domain.ReclassIgnore, "none", "not_mentioned":
		return domain.ReclassIgnore, nil
	case domain.ReclassInclude, "include", "include_all", "included", "add":
		return domain.ReclassInclude, nil
	case domain.ReclassExclude, "exclude", "excluded", "remove":
		return domain.ReclassExclude, nil
	case domain.ReclassBoth, "include_and_exclude", "net", "auditor_allocation":
		return domain.ReclassBoth, nil
	}
	return "", fmt.Errorf("reclassification %q is not one of include_in, exclude_from, both, ignore", v)
}

var _entitySources = map[string]bool{
	"kyc": true, "corporate_structure": true, "compliance_file": true, "ias24": true,
}

func normaliseEntitySource(kind domain.TermKind, v string) string {
	if kind != domain.TermRelatedPartyPayments {
		return ""
	}
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "kyc_dossier", "kyc dossier", "know your customer":
		return "kyc"
	case "ias 24", "ias-24", "мсфо 24":
		return "ias24"
	case "compliance", "compliance_dossier":
		return "compliance_file"
	}
	if _entitySources[v] {
		return v
	}
	return ""
}

func normaliseEntityScope(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "any", "all", "none":
		return "", nil
	case domain.StatusUnrestricted, "unrestricted_subsidiaries", "outside_security":
		return domain.StatusUnrestricted, nil
	case domain.StatusRestricted, "restricted_subsidiaries", "inside_security":
		return domain.StatusRestricted, nil
	}
	return "", fmt.Errorf("entity_scope %q is not one of %s, %s or empty",
		v, domain.StatusRestricted, domain.StatusUnrestricted)
}

func normaliseTermCategory(v string) (domain.Category, error) {
	c := domain.Category(strings.ToLower(strings.TrimSpace(v)))
	switch {
	case c == "" || c == domain.CatUnknown:
		return "", nil
	case domain.ValidCategory(c):
		return c, nil
	}
	return "", fmt.Errorf("category %q is not one of the taxonomy", v)
}

func validTermKind(k domain.TermKind) bool {
	switch k {
	case domain.TermStatementLine, domain.TermStatementNote, domain.TermLedgerCategory,
		domain.TermRelatedPartyPayments, domain.TermGroupConsolidated, domain.TermConstant:
		return true
	}
	return false
}

var _identRe = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*`)

func checkExpressionTerms(spec *domain.CovenantSpec) error {
	defined := make(map[string]bool, len(spec.Terms))
	for _, t := range spec.Terms {
		defined[t.Name] = true
	}
	used := make(map[string]bool, len(spec.Terms))
	for _, expr := range expressionsOf(spec) {
		for _, id := range _identRe.FindAllString(expr, -1) {
			if id == "max" || id == "min" {
				continue
			}
			used[id] = true
			if !defined[id] {
				return fmt.Errorf("expression uses %q, which is not defined in terms", id)
			}
		}
	}
	var unused []string
	for _, t := range spec.Terms {
		if !used[t.Name] {
			unused = append(unused, t.Name)
		}
	}
	if len(unused) > 0 {
		return fmt.Errorf("terms %s are defined but never used", strings.Join(unused, ", "))
	}
	return nil
}

func expressionsOf(spec *domain.CovenantSpec) []string {
	out := []string{spec.Expression}
	if spec.Trigger != nil {
		out = append(out, spec.Trigger.Expression)
	}
	for _, c := range spec.Carveouts {
		if c.Condition.Expression != "" {
			out = append(out, c.Condition.Expression)
		}
	}
	return out
}
