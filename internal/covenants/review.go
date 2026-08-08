package covenants

import (
	"fmt"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/index"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

func Review(st *store.Store, scenarioID string) (string, error) {
	var specs []*domain.CovenantSpec
	ok, err := st.GetArtifact(ArtifactKind, scenarioID, &specs)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no specifications for %s; run `halyk-agent covenants` first", scenarioID)
	}

	var idx index.Index
	if _, err := st.GetArtifact(index.ArtifactKind, index.ArtifactID, &idx); err != nil {
		return "", err
	}

	clauseText := make(map[string]string, len(specs))
	if agreements := idx.CreditAgreements(scenarioID); len(agreements) > 0 {
		if text, err := st.DocText(agreements[0].DocID); err == nil {
			var ids []string
			for _, s := range specs {
				ids = append(ids, s.ClauseID)
			}
			if article, err := CovenantArticleFor(text, ids); err == nil {
				for _, id := range ids {
					if body, err := Clause(article.Text, id); err == nil {
						clauseText[id] = body
					}
				}
			}
		}
	}

	var b strings.Builder
	rule := strings.Repeat("═", 100)
	for _, s := range specs {
		fmt.Fprintf(&b, "\n%s\n%s / %s — %s\n%s\n", rule, s.ScenarioID, s.ClauseID, s.Title, rule)

		if txt := clauseText[s.ClauseID]; txt != "" {
			fmt.Fprintf(&b, "\nCONTRACT TEXT\n%s\n", indent(wrap(collapse(txt), 96), "  "))
		}

		fmt.Fprintf(&b, "\nSPECIFICATION\n")
		fmt.Fprintf(&b, "  metric      %s %s %s  (%s)\n", s.Expression, s.Op, s.Threshold.String(), s.Unit)
		fmt.Fprintf(&b, "  period      %s  %s .. %s\n", s.Period.Kind,
			dateOf(s.Period.From), dateOf(s.Period.To))
		fmt.Fprintf(&b, "  evidence    %s\n", s.EvidenceKind)
		for _, t := range s.Terms {
			fmt.Fprintf(&b, "  term %-22s %-24s line=%q\n", t.Name, t.Kind, t.Line)
			if t.Reclassification != "" && t.Reclassification != domain.ReclassIgnore {
				fmt.Fprintf(&b, "       %-22s reclassification=%s\n", "", t.Reclassification)
			}
			if t.EntitySource != "" {
				fmt.Fprintf(&b, "       %-22s entity_source=%s\n", "", t.EntitySource)
			}
			if t.Description != "" {
				fmt.Fprintf(&b, "       %-22s %s\n", "", truncate(collapse(t.Description), 74))
			}
		}
		if s.Trigger != nil {
			fmt.Fprintf(&b, "  TRIGGER     %s\n", s.Trigger.Expression)
			fmt.Fprintf(&b, "              %s\n", truncate(collapse(s.Trigger.Description), 84))
		}
		for i, c := range s.Carveouts {
			fmt.Fprintf(&b, "  CARVE-OUT %d %s\n", i+1, truncate(collapse(c.Description), 84))
			if c.Condition.Expression != "" {
				fmt.Fprintf(&b, "              when %s\n", c.Condition.Expression)
			}
		}
		fmt.Fprintf(&b, "  quote       %s\n", truncate(collapse(s.SourceRef.Quote), 88))
		fmt.Fprintf(&b, "  confidence  %.2f   critic passes: %d\n", s.Confidence, s.CriticPasses)
		for _, n := range s.CriticNotes {
			fmt.Fprintf(&b, "  critic      %s\n", truncate(collapse(n), 88))
		}
	}
	return b.String(), nil
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func dateOf(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func wrap(s string, width int) string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line == "" {
			line = w
			continue
		}
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	if line != "" {
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
