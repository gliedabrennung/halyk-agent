package engine

import (
	"slices"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func Evidence(spec *domain.CovenantSpec, in *Inputs, verdict domain.Verdict) (*string, []string) {

	scoped := scopeToPeriod(in, spec.Period)
	var candidates []string
	for i := range scoped.Txns {
		trimmed := *scoped
		trimmed.Txns = slices.Concat(scoped.Txns[:i], scoped.Txns[i+1:])

		alt, err := Evaluate(spec, &trimmed)
		if err != nil {
			continue
		}

		if alt.MetricUndefined {
			continue
		}
		if alt.Status != verdict.Status {
			candidates = append(candidates, scoped.Txns[i].ID)
		}
	}

	switch len(candidates) {
	case 0:
		return nil, nil
	case 1:
		id := candidates[0]
		return &id, candidates
	}

	var reclassified []string
	if in.Labels != nil {
		byID := make(map[string]domain.TxnLabel, len(in.Labels.Txns))
		for _, l := range in.Labels.Txns {
			byID[l.TxnID] = l
		}
		for _, id := range candidates {
			if l, ok := byID[id]; ok && (l.Reclassified || l.AdjustmentKind != "") {
				reclassified = append(reclassified, id)
			}
		}
	}
	if len(reclassified) == 1 {
		id := reclassified[0]
		return &id, candidates
	}
	return nil, candidates
}
