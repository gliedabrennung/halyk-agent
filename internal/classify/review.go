package classify

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/shopspring/decimal"
)

func Review(st *store.Store, scenarioID string) (string, error) {
	var set domain.LabelSet
	ok, err := st.GetArtifact(ArtifactKind, scenarioID, &set)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no labels for %s; run `halyk-agent classify` first", scenarioID)
	}
	amounts, err := amountsFor(st, scenarioID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	rule := strings.Repeat("═", 108)
	fmt.Fprintf(&b, "\n%s\n%s — %s   %d rows, %d to related parties\n%s\n",
		rule, set.ScenarioID, set.Company, len(set.Txns), countRelated(&set), rule)

	if len(set.RelatedParties) > 0 {
		fmt.Fprintf(&b, "\nRELATED PARTIES (at or above this borrower's own threshold)\n")
		for _, name := range set.RelatedParties {
			fmt.Fprintf(&b, "  %s\n", name)
		}
	}
	for _, name := range set.UnmatchedParties {
		fmt.Fprintf(&b, "  %s — no ledger row under this name\n", name)
	}

	fmt.Fprintf(&b, "\nTOTALS BY CATEGORY (ledger signs: outflow negative)\n")
	for _, c := range slices.Sorted(maps.Keys(set.Totals)) {
		n := len(set.ByCategory(c))
		fmt.Fprintf(&b, "  %-22s %18s  (%d rows)\n", c, set.Totals[c].StringFixed(2), n)
	}

	fmt.Fprintf(&b, "\nROWS\n")
	fmt.Fprintf(&b, "  %-16s %-22s %16s %-7s %s\n", "txn", "category", "amount", "flags", "counterparty / pattern")
	rows := slices.Clone(set.Txns)
	slices.SortFunc(rows, func(a, b domain.TxnLabel) int { return strings.Compare(a.TxnID, b.TxnID) })
	for _, t := range rows {
		var flags []string
		if t.RelatedParty {
			flags = append(flags, "RP")
		}
		if t.Contra {
			flags = append(flags, "contra")
		}
		if t.Reclassified {
			flags = append(flags, "recls")
		} else if t.AdjustmentKind != "" {
			flags = append(flags, t.AdjustmentKind)
		}
		fmt.Fprintf(&b, "  %-16s %-22s %16s %-7s %s | %s\n",
			t.TxnID, t.Category, amounts[t.TxnID].StringFixed(2), strings.Join(flags, ","),
			truncateRunes(t.Counterparty, 34), t.Pattern)
	}
	fmt.Fprintf(&b, "%s\n", rule)
	return b.String(), nil
}

func amountsFor(st *store.Store, scenarioID string) (map[string]decimal.Decimal, error) {
	txns, err := st.LoadTxns()
	if err != nil {
		return nil, err
	}
	out := make(map[string]decimal.Decimal, len(txns))
	for _, t := range txns {
		if t.ScenarioID == scenarioID {
			out[t.ID] = t.Amount
		}
	}
	return out, nil
}

func countRelated(set *domain.LabelSet) int {
	n := 0
	for _, t := range set.Txns {
		if t.RelatedParty {
			n++
		}
	}
	return n
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
