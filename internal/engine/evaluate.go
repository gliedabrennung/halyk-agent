package engine

import (
	"fmt"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/shopspring/decimal"
)

func Evaluate(spec *domain.CovenantSpec, in *Inputs) (domain.Verdict, error) {
	scoped := scopeToPeriod(in, spec.Period)

	vars, terms, err := computeTerms(spec, scoped)
	if err != nil {
		return domain.Verdict{}, err
	}

	const contestedConfidence = 0.4

	v := domain.Verdict{
		ScenarioID: spec.ScenarioID,
		ClauseID:   spec.ClauseID,
		Source:     SourceEngine,
		Confidence: spec.Confidence,
	}
	for _, t := range terms {
		v.Trace = append(v.Trace, t.Trace)
		v.Contributors = append(v.Contributors, t.Contributors...)
		if t.Unmeasurable {

			v.Confidence = 0.2
		}
		if t.Contested {

			v.Confidence = min(v.Confidence, contestedConfidence)
		}
	}
	v.Contributors = sortedIDs(v.Contributors)

	metric, err := EvalExpr(spec.Expression, vars)
	if err != nil {
		if IsDivByZero(err) {

			v.Status = domain.StatusCompliant
			v.Actual = decimal.Zero
			v.Confidence = 0
			v.MetricUndefined = true
			v.Trace = append(v.Trace,
				"metric "+spec.Expression+": denominator is zero, so the ratio is undefined")
			return v, nil
		}
		return domain.Verdict{}, fmt.Errorf("%s/%s: %w", spec.ScenarioID, spec.ClauseID, err)
	}

	v.Actual = metric.Abs().Round(2)
	v.Trace = append(v.Trace, fmt.Sprintf("metric %s = %s (threshold %s %s)",
		spec.Expression, metric.StringFixed(4), spec.Op, spec.Threshold))

	if spec.Trigger != nil && strings.TrimSpace(spec.Trigger.Expression) != "" {
		fired, err := EvalCondition(spec.Trigger.Expression, vars)
		if err != nil {
			v.Trace = append(v.Trace, "trigger "+spec.Trigger.Expression+
				" could not be evaluated, so the covenant is tested anyway: "+err.Error())
			v.Confidence = min(v.Confidence, 0.3)
			fired = true
			v.TriggerFired = true
		} else {
			v.TriggerFired = fired
			v.Trace = append(v.Trace, fmt.Sprintf("trigger %s = %v", spec.Trigger.Expression, fired))
		}
		if !fired {
			v.Status = domain.StatusCompliant
			v.Trace = append(v.Trace, "the covenant does not apply in this period")
			return v, nil
		}
	} else {
		v.TriggerFired = true
	}

	v.Status = domain.StatusCompliant
	if !Compare(metric, spec.Op, spec.Threshold) {
		v.Status = domain.StatusBreach
		if name, ok := carveoutApplies(spec, metric, vars, &v); ok {
			v.Status = domain.StatusCompliant
			v.CarveoutApplied = name
			v.Trace = append(v.Trace, "carve-out applies: "+name)
		}
	}
	return v, nil
}

func carveoutApplies(
	spec *domain.CovenantSpec,
	metric decimal.Decimal,
	vars map[string]decimal.Decimal,
	v *domain.Verdict,
) (string, bool) {
	for _, c := range spec.Carveouts {
		expr := strings.TrimSpace(c.Condition.Expression)
		if expr == "" {
			continue
		}
		ok, err := EvalCondition(expr, vars)
		if err != nil {
			v.Trace = append(v.Trace, "carve-out "+expr+" could not be evaluated: "+err.Error())
			v.Confidence = min(v.Confidence, 0.3)
			continue
		}
		if !ok {
			continue
		}
		name := c.Description
		if name == "" {
			name = expr
		}
		if excess, capped := overCap(spec, c, metric); capped {
			v.Trace = append(v.Trace, fmt.Sprintf(
				"carve-out %q reaches %s but the threshold is passed by %s, so it does not cover this breach",
				name, c.Cap.Abs().StringFixed(2), excess.StringFixed(2)))
			continue
		}
		if c.Cap.IsPositive() && spec.Unit != domain.UnitUSD {
			v.Trace = append(v.Trace, fmt.Sprintf(
				"carve-out %q states a cap of %s, which cannot be checked against a %s metric",
				name, c.Cap.Abs().StringFixed(2), spec.Unit))
			v.Confidence = min(v.Confidence, 0.3)
		}
		return name, true
	}
	return "", false
}

func overCap(
	spec *domain.CovenantSpec,
	c domain.Carveout,
	metric decimal.Decimal,
) (decimal.Decimal, bool) {
	if !c.Cap.IsPositive() {
		return decimal.Zero, false
	}
	if spec.Unit != domain.UnitUSD {
		return decimal.Zero, false
	}
	excess := metric.Sub(spec.Threshold).Abs()
	return excess, excess.GreaterThan(c.Cap.Abs())
}
