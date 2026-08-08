package classify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/llm"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

// Модель недоступна (ключа нет), поэтому и основной батч, и эскалация обязаны упасть.
// Стадия при этом должна не вернуть ошибку, а разметить шаблоны keyword-правилами.
func TestPatternBatchFailureFallsBackToRules(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{Model: "test-model", MaxConcurrency: 2, LogDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := Options{Cfg: cfg, Store: st, Log: log, Client: llm.New(cfg, st, log)}

	patterns := []*patternInfo{
		{pattern: "payroll settlement", total: 3},
		{pattern: "quarterly rent payment", total: 2},
		{pattern: "unmappable gibberish", total: 1},
	}
	rep := &Report{}

	labels, err := labelPatterns(context.Background(), opts, patterns, rep)
	if err != nil {
		t.Fatalf("a dead model must degrade the stage, not fail it: %v", err)
	}
	if len(rep.Failed) == 0 {
		t.Error("the failed batch must be reported")
	}
	if len(labels) != len(patterns) {
		t.Fatalf("labels = %d, want %d", len(labels), len(patterns))
	}

	want := []struct {
		cat    domain.Category
		source string
	}{
		{domain.CatPayroll, "rule"},
		{domain.CatRent, "rule"},
		{domain.CatUnknown, "llm"},
	}
	for i, w := range want {
		if labels[i].Category != w.cat {
			t.Errorf("%q: category = %q, want %q", patterns[i].pattern, labels[i].Category, w.cat)
		}
		if labels[i].Source != w.source {
			t.Errorf("%q: source = %q, want %q", patterns[i].pattern, labels[i].Source, w.source)
		}
	}
	if rep.Unknown != 0 {
		t.Errorf("Unknown is counted per transaction, not per pattern; got %d", rep.Unknown)
	}
}
