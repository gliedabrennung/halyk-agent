package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/shopspring/decimal"
)

const FactsSchemaVersion = "facts-v2"

type FactsInput struct {
	ScenarioID string
	Company    string

	Documents []FactsDoc

	GroupDoc *FactsDoc

	MissingAmounts []string
}

type FactsDoc struct {
	DocID   string
	DocType string
	Text    string

	OCRPages []int
	OCRText  string
}

type factsJSON struct {
	Adjustments []struct {
		Kind         string `json:"kind"`
		TxnID        string `json:"txn_id"`
		Counterparty string `json:"counterparty"`
		Amount       string `json:"amount"`
		FromCategory string `json:"from_category"`
		ToCategory   string `json:"to_category"`
		Rationale    string `json:"rationale"`
		Applied      *bool  `json:"applied"`
		SourceDoc    string `json:"source_doc"`
		Quote        string `json:"quote"`
	} `json:"adjustments"`
	Parties []struct {
		Name         string `json:"name"`
		VotingShare  string `json:"voting_share"`
		PledgedShare string `json:"pledged_share"`
		Relation     string `json:"relation"`
		Status       string `json:"status"`
		SourceDoc    string `json:"source_doc"`
		Quote        string `json:"quote"`
	} `json:"parties"`
	RelatedPartyThreshold string `json:"related_party_threshold"`
	UnrestrictedThreshold string `json:"unrestricted_threshold"`
	FXRates               []struct {
		Currency string `json:"currency"`
		USDRate  string `json:"usd_rate"`
		Basis    string `json:"basis"`
		Quote    string `json:"quote"`
	} `json:"fx_rates"`
	GroupPPE *struct {
		Parent          string `json:"parent"`
		Period          string `json:"period"`
		Opening         string `json:"opening"`
		Closing         string `json:"closing"`
		Depreciation    string `json:"depreciation"`
		Disposals       string `json:"disposals"`
		DisposalsStated *bool  `json:"disposals_stated"`
		SourceDoc       string `json:"source_doc"`
		Quote           string `json:"quote"`
	} `json:"group_ppe"`
	Notes      []string `json:"notes"`
	Confidence float64  `json:"confidence"`
}

const _factsRepairPrompt = "Your previous answer was rejected: %v\n\nIt was:\n%s\n\n" +
	"Return corrected STRICT JSON for the same borrower.\n\n%s"

func ExtractFacts(
	ctx context.Context,
	client *llm.Client,
	model string,
	in FactsInput,
) (*domain.FactBase, error) {
	req := llm.Request{
		Name:          "fact_base",
		Model:         model,
		Description:   "Transcribes auditor disclosures and ownership tables for one borrower.",
		Instruction:   _factsInstruction,
		Prompt:        in.prompt(),
		SchemaVersion: FactsSchemaVersion,
		JSON:          true,
	}
	return completeWithRepair(ctx, client, req, in.ScenarioID+" facts",
		func(cause error, raw string) string {
			return fmt.Sprintf(_factsRepairPrompt, cause, raw, in.prompt())
		},
		func(raw string) (*domain.FactBase, error) { return parseFacts(raw, in) })
}

func parseFacts(raw string, in FactsInput) (*domain.FactBase, error) {
	var fj factsJSON
	if err := json.Unmarshal([]byte(stripFence(raw)), &fj); err != nil {
		return nil, fmt.Errorf("decode facts json: %w", err)
	}

	fb := &domain.FactBase{
		ScenarioID: in.ScenarioID,
		Company:    in.Company,
		Notes:      fj.Notes,
		Confidence: fj.Confidence,
		FXRates:    make(map[string]decimal.Decimal, len(fj.FXRates)),
	}
	for _, d := range in.Documents {
		fb.SourceDocs = append(fb.SourceDocs, d.DocID)
	}

	for _, a := range fj.Adjustments {
		kind := strings.TrimSpace(a.Kind)
		if !validAdjustmentKind(kind) {
			return nil, fmt.Errorf("adjustment kind %q is not allowed", kind)
		}
		adj := domain.Adjustment{
			Kind:         kind,
			TxnID:        strings.TrimSpace(a.TxnID),
			Counterparty: strings.TrimSpace(a.Counterparty),
			FromCategory: strings.TrimSpace(a.FromCategory),
			ToCategory:   strings.TrimSpace(a.ToCategory),
			Rationale:    strings.TrimSpace(a.Rationale),
			SourceRef:    domain.PageRef{DocID: strings.TrimSpace(a.SourceDoc), Quote: strings.TrimSpace(a.Quote)},
		}

		switch {
		case a.Applied != nil:
			adj.Applied = *a.Applied
		case kind == domain.AdjNoChange:
			adj.Applied = false
		default:
			adj.Applied = true
		}
		if kind == domain.AdjNoChange {
			adj.Applied = false
		}
		if s := strings.TrimSpace(a.Amount); s != "" {
			amt, err := parseDecimal(s)
			if err != nil {
				return nil, fmt.Errorf("adjustment amount %q: %w", s, err)
			}
			adj.Amount = amt
		}

		if adj.Kind == domain.AdjDisclosedAmount && adj.TxnID != "" {
			adj.Kind = domain.AdjLedgerAmountFix
		}
		if adj.Applied && adj.Amount.IsZero() && adj.TxnID == "" {
			return nil, fmt.Errorf("adjustment %q names neither an amount nor a transaction", kind)
		}
		fb.Adjustments = append(fb.Adjustments, adj)
	}

	for _, p := range fj.Parties {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		party := domain.Party{
			Name:      name,
			Relation:  strings.TrimSpace(p.Relation),
			Status:    strings.TrimSpace(p.Status),
			SourceRef: domain.PageRef{DocID: strings.TrimSpace(p.SourceDoc), Quote: strings.TrimSpace(p.Quote)},
		}
		var err error
		if party.VotingShare, err = parsePercent(p.VotingShare); err != nil {
			return nil, fmt.Errorf("voting share %q for %s: %w", p.VotingShare, name, err)
		}
		if party.PledgedShare, err = parsePercent(p.PledgedShare); err != nil {
			return nil, fmt.Errorf("pledged share %q for %s: %w", p.PledgedShare, name, err)
		}
		fb.Parties = append(fb.Parties, party)
	}

	var err error
	if fb.RelatedPartyThreshold, err = parsePercent(fj.RelatedPartyThreshold); err != nil {
		return nil, fmt.Errorf("related_party_threshold %q: %w", fj.RelatedPartyThreshold, err)
	}
	if fb.UnrestrictedThreshold, err = parsePercent(fj.UnrestrictedThreshold); err != nil {
		return nil, fmt.Errorf("unrestricted_threshold %q: %w", fj.UnrestrictedThreshold, err)
	}

	if fb.RelatedPartyThreshold.IsPositive() {
		for i := range fb.Parties {
			fb.Parties[i].Related = fb.Parties[i].VotingShare.GreaterThanOrEqual(fb.RelatedPartyThreshold)
		}
	}

	if fb.UnrestrictedThreshold.IsPositive() {
		for i := range fb.Parties {
			if fb.Parties[i].PledgedShare.IsZero() {
				continue
			}
			if fb.Parties[i].PledgedShare.LessThan(fb.UnrestrictedThreshold) {
				fb.Parties[i].Status = domain.StatusUnrestricted
			} else {
				fb.Parties[i].Status = domain.StatusRestricted
			}
		}
	}

	if g := fj.GroupPPE; g != nil {
		ppe := &domain.GroupPPE{
			Parent:    strings.TrimSpace(g.Parent),
			Period:    strings.TrimSpace(g.Period),
			SourceRef: domain.PageRef{DocID: strings.TrimSpace(g.SourceDoc), Quote: strings.TrimSpace(g.Quote)},
		}
		for _, f := range []struct {
			name string
			raw  string
			dst  *decimal.Decimal
		}{
			{"opening", g.Opening, &ppe.Opening},
			{"closing", g.Closing, &ppe.Closing},
			{"depreciation", g.Depreciation, &ppe.Depreciation},
			{"disposals", g.Disposals, &ppe.Disposals},
		} {
			s := strings.TrimSpace(f.raw)
			if s == "" {
				continue
			}
			v, err := parseDecimal(s)
			if err != nil {
				return nil, fmt.Errorf("group_ppe %s %q: %w", f.name, s, err)
			}
			*f.dst = v.Abs()
		}
		ppe.DisposalsStated = g.DisposalsStated != nil && *g.DisposalsStated
		if !ppe.Opening.IsZero() || !ppe.Closing.IsZero() {
			fb.GroupPPE = ppe
		}
	}

	for _, r := range fj.FXRates {
		cur := strings.ToUpper(strings.TrimSpace(r.Currency))
		if cur == "" || cur == "USD" {
			continue
		}
		if strings.TrimSpace(r.USDRate) == "" {
			continue
		}
		rate, err := parseDecimal(r.USDRate)
		if err != nil {
			return nil, fmt.Errorf("fx rate for %s (%q): %w", cur, r.USDRate, err)
		}
		if !rate.IsPositive() {
			return nil, fmt.Errorf("fx rate for %s is not positive: %s", cur, rate)
		}
		fb.FXRates[cur] = rate
		if r.Basis != "" {
			fb.Notes = append(fb.Notes, fmt.Sprintf("FX %s=%s: %s", cur, rate, strings.TrimSpace(r.Basis)))
		}
	}
	return fb, nil
}

func validAdjustmentKind(k string) bool {
	switch k {
	case domain.AdjReclassify, domain.AdjExcludePeriod, domain.AdjIncludePeriod,
		domain.AdjDisclosedAmount, domain.AdjLedgerAmountFix, domain.AdjEBITDAAddBack, domain.AdjNoChange:
		return true
	}
	return false
}
