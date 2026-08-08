package evaluate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gliedabrennung/halyk-agent/internal/agents"
	"github.com/gliedabrennung/halyk-agent/internal/covenants"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/engine"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"golang.org/x/sync/errgroup"
)

const _criticConfidence = 0.35

type cell struct {
	spec    *domain.CovenantSpec
	verdict domain.Verdict
	inputs  *engine.Inputs
}

type review struct {
	agrees  bool
	concern string
	issue   string
}

func criticise(ctx context.Context, opts Options, cells []cell) (map[string]review, error) {
	if opts.Client == nil || len(cells) == 0 {
		return nil, nil
	}

	var idx index.Index
	if _, err := opts.Store.GetArtifact(index.ArtifactKind, index.ArtifactID, &idx); err != nil {
		return nil, err
	}
	clauseText := clauseTexts(opts.Store, &idx, cells)

	out := make(map[string]review, len(cells))
	var (
		mu        sync.Mutex
		quotaOnce sync.Once
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.Cfg.MaxConcurrency)

	for _, c := range cells {
		g.Go(func() error {
			in := criticInput(c, clauseText[store.VerdictKey(c.spec.ScenarioID, c.spec.ClauseID)])
			res, err := agents.ReviewVerdict(gctx, opts.Client, opts.Cfg.Model, in)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}

				if llm.IsQuotaExhausted(err) {
					quotaOnce.Do(func() {
						opts.Log.Error("the model's daily quota is exhausted; cells are computed but unreviewed")
					})
					return nil
				}
				opts.Log.Warn("cell review failed",
					"scenario", c.spec.ScenarioID, "clause", c.spec.ClauseID, "err", err)
				return nil
			}
			mu.Lock()
			out[store.VerdictKey(c.spec.ScenarioID, c.spec.ClauseID)] = review{
				agrees: res.Agrees, concern: res.Concern, issue: res.Issue,
			}
			mu.Unlock()
			if !res.Agrees {
				opts.Log.Warn("critic disagrees", "scenario", c.spec.ScenarioID, "clause", c.spec.ClauseID,
					"issue", res.Issue, "concern", res.Concern)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func criticInput(c cell, text string) agents.VerdictCriticInput {
	in := agents.VerdictCriticInput{
		ScenarioID: c.spec.ScenarioID,
		ClauseID:   c.spec.ClauseID,
		ClauseText: text,
		Metric:     fmt.Sprintf("%s %s %s", c.spec.Expression, c.spec.Op, c.spec.Threshold),
		Status:     c.verdict.Status,
		Actual:     c.verdict.Actual.StringFixed(2),
		Evidence:   evidenceText(c.verdict.EvidenceID),
		Terms:      c.verdict.Trace,
	}
	if c.inputs != nil && c.inputs.Facts != nil {
		fb := c.inputs.Facts
		in.Company = fb.Company
		if fb.RelatedPartyThreshold.IsPositive() {
			in.Parties = append(in.Parties, fmt.Sprintf(
				"a counterparty is related at or above %s%% of the voting rights", fb.RelatedPartyThreshold))
		}
		if fb.UnrestrictedThreshold.IsPositive() {
			in.Parties = append(in.Parties, fmt.Sprintf(
				"a subsidiary is unrestricted below %s%% of its assets pledged", fb.UnrestrictedThreshold))
		}
		for _, p := range fb.Parties {
			line := "  " + p.Name
			if p.VotingShare.IsPositive() {
				line += fmt.Sprintf(", %s%% of the votes", p.VotingShare)
			}
			if p.PledgedShare.IsPositive() {
				line += fmt.Sprintf(", %s%% of its assets pledged", p.PledgedShare)
			}
			if p.Related {
				line += " — RELATED"
			} else if p.VotingShare.IsPositive() {
				line += " — not related"
			}
			if p.Status != "" {
				line += " — " + p.Status
			}
			in.Parties = append(in.Parties, line)
		}
		for _, a := range fb.Adjustments {
			state := "APPLIED"
			if !a.Applied {
				state = "REJECTED by the auditor"
			}
			in.Disclosures = append(in.Disclosures, fmt.Sprintf("%s %s: %s %s %s (%s)",
				state, a.Kind, a.TxnID, a.Counterparty, a.Amount.StringFixed(2), a.Rationale))
		}
	}
	if c.spec.Period.From.IsZero() {
		in.Period = c.spec.Period.Kind
	} else {
		in.Period = fmt.Sprintf("%s %s .. %s", c.spec.Period.Kind,
			c.spec.Period.From.Format("2006-01-02"), c.spec.Period.To.Format("2006-01-02"))
	}
	in.Rows = contributorRows(c)
	return in
}

func contributorRows(c cell) []string {
	const maxCriticRows = 12
	if c.inputs == nil {
		return nil
	}
	wanted := make(map[string]bool)
	for _, id := range c.verdict.Contributors {
		wanted[id] = true
	}
	labels := make(map[string]domain.TxnLabel)
	if c.inputs.Labels != nil {
		for _, l := range c.inputs.Labels.Txns {
			labels[l.TxnID] = l
		}
	}

	var picked []domain.Txn
	for _, t := range c.inputs.Txns {
		if wanted[t.ID] {
			picked = append(picked, t)
		}
	}
	sort.Slice(picked, func(i, j int) bool {
		return picked[i].AmountUSD.Abs().GreaterThan(picked[j].AmountUSD.Abs())
	})
	if len(picked) > maxCriticRows {
		picked = picked[:maxCriticRows]
	}

	var out []string
	for _, t := range picked {
		l := labels[t.ID]
		flags := ""
		if l.RelatedParty {
			flags += " [related party]"
		}
		if l.Contra {
			flags += " [reverses a cost]"
		}
		if l.Reclassified {
			flags += " [reclassified by the auditor to " + string(l.ReclassifiedTo) + "]"
		}
		out = append(out, fmt.Sprintf("%s %s %16s %s — %s (%s)%s",
			t.ID, t.Date.Format("2006-01-02"), t.AmountUSD.StringFixed(2), l.Category,
			t.Description, t.Counterparty, flags))
	}
	return out
}

func clauseTexts(st *store.Store, idx *index.Index, cells []cell) map[string]string {
	byScenario := make(map[string][]string, len(cells))
	for _, c := range cells {
		byScenario[c.spec.ScenarioID] = append(byScenario[c.spec.ScenarioID], c.spec.ClauseID)
	}

	out := make(map[string]string, len(cells))
	for scn, clauses := range byScenario {
		agreements := idx.CreditAgreements(scn)
		if len(agreements) == 0 {
			continue
		}
		text, err := st.DocText(agreements[0].DocID)
		if err != nil {
			continue
		}
		article, err := covenants.CovenantArticleFor(text, clauses)
		if err != nil {
			continue
		}
		for _, id := range clauses {
			body, err := covenants.Clause(article.Text, id)
			if err != nil {
				continue
			}
			out[store.VerdictKey(scn, id)] = strings.TrimSpace(body)
		}
	}
	return out
}
