package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/shopspring/decimal"
)

func completeWithRepair[T any](
	ctx context.Context,
	client *llm.Client,
	req llm.Request,
	label string,
	repairPrompt func(cause error, raw string) string,
	parse func(raw string) (T, error),
) (T, error) {
	var zero T
	raw, err := client.Complete(ctx, req)
	if err != nil {
		return zero, err
	}
	out, cause := parse(raw)
	if cause == nil {
		return out, nil
	}

	repair := req
	repair.SchemaVersion += "-repair"
	repair.Prompt = repairPrompt(cause, raw)
	raw, err = client.Complete(ctx, repair)
	if err != nil {
		return zero, fmt.Errorf("%s: %w (repair call also failed: %v)", label, cause, err)
	}
	out, err = parse(raw)
	if err != nil {
		return zero, fmt.Errorf("%s: unusable after repair: %w", label, err)
	}
	return out, nil
}

func parsePercent(raw string) (decimal.Decimal, error) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	if s == "" {
		return decimal.Zero, nil
	}
	return parseDecimal(s)
}

// isTrue reads a flag a model may render as a JSON boolean or as a word.
func isTrue(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "y", "1":
			return true
		}
	}
	return false
}

func parseDecimal(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, errors.New("empty")
	}

	s = strings.NewReplacer("$", "", " ", "", ",", "", " ", "", "x", "", "X", "").Replace(s)
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, errors.New("not a decimal")
	}
	return d, nil
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("not a date")
}

func stripFence(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	return s
}
