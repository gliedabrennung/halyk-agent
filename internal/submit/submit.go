package submit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/domain"
	"github.com/gliedabrennung/halyk-agent/internal/engine"
	"github.com/gliedabrennung/halyk-agent/internal/ingest"
	"github.com/gliedabrennung/halyk-agent/internal/store"
	"github.com/tidwall/sjson"
)

type Options struct {
	Cfg   *config.Config
	Store *store.Store
	Log   *slog.Logger
}

type Result struct {
	Path         string
	AuditPath    string
	Cells        int
	FromEngine   int
	FromBaseline int
}

func Run(opts Options) (*Result, error) {
	tpl, txns, err := ingest.LoadTemplateAndTxns(opts.Cfg, opts.Store)
	if err != nil {
		return nil, err
	}
	led := domain.NewLedger(txns)

	verdicts, err := opts.Store.LoadVerdicts()
	if err != nil {
		return nil, err
	}

	baseline, err := engine.BaselineVerdicts(tpl, led)
	if err != nil {
		return nil, err
	}
	res := &Result{Cells: len(tpl.Cells)}
	filled := make([]domain.Verdict, 0, len(tpl.Cells))
	for _, b := range baseline {
		if v, ok := verdicts[store.VerdictKey(b.ScenarioID, b.ClauseID)]; ok {
			filled = append(filled, v)
			res.FromEngine++
			continue
		}
		filled = append(filled, b)
		res.FromBaseline++
	}
	if res.FromBaseline > 0 {
		opts.Log.Warn("cells answered by the baseline placeholder, not by the engine",
			"baseline", res.FromBaseline, "engine", res.FromEngine, "total", res.Cells)
	}

	raw, err := fill(tpl, filled, opts.Cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	res.Path = opts.Cfg.SubmissionPath()
	if err := os.WriteFile(res.Path, raw, 0o644); err != nil {
		return nil, fmt.Errorf("write submission: %w", err)
	}

	res.AuditPath = filepath.Join(opts.Cfg.OutDir, "audit.jsonl")
	if err := writeAudit(res.AuditPath, filled); err != nil {
		return nil, err
	}
	return res, nil
}

func fill(tpl *domain.Template, verdicts []domain.Verdict, cfg *config.Config) ([]byte, error) {
	raw := append([]byte(nil), tpl.Raw...)
	var err error

	for key, value := range map[string]string{
		"team":          cfg.Team,
		"contact_email": cfg.ContactEmail,
		"model":         cfg.Model,
	} {
		if raw, err = sjson.SetBytes(raw, key, value); err != nil {
			return nil, fmt.Errorf("set %s: %w", key, err)
		}
	}

	for _, v := range verdicts {
		base := "answers." + EscapeKey(v.ScenarioID) + "." + EscapeKey(v.ClauseID)

		if v.Status != domain.StatusCompliant && v.Status != domain.StatusBreach {
			return nil, fmt.Errorf("%s/%s: status %q is neither %s nor %s",
				v.ScenarioID, v.ClauseID, v.Status, domain.StatusCompliant, domain.StatusBreach)
		}
		if raw, err = sjson.SetBytes(raw, base+".status", v.Status); err != nil {
			return nil, fmt.Errorf("set %s.status: %w", base, err)
		}

		actual := v.Actual.Abs().Round(2)
		if raw, err = sjson.SetRawBytes(raw, base+".actual", []byte(actual.StringFixed(2))); err != nil {
			return nil, fmt.Errorf("set %s.actual: %w", base, err)
		}

		if v.EvidenceID == nil {
			raw, err = sjson.SetRawBytes(raw, base+".evidence_txn_id", []byte("null"))
		} else {
			raw, err = sjson.SetBytes(raw, base+".evidence_txn_id", *v.EvidenceID)
		}
		if err != nil {
			return nil, fmt.Errorf("set %s.evidence_txn_id: %w", base, err)
		}
	}
	return raw, nil
}

func EscapeKey(k string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`.`, `\.`,
		`*`, `\*`,
		`?`, `\?`,
		`#`, `\#`,
		`|`, `\|`,
		`@`, `\@`,
	)
	return r.Replace(k)
}

func writeAudit(path string, verdicts []domain.Verdict) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create audit: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, v := range verdicts {
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("write audit line %s/%s: %w", v.ScenarioID, v.ClauseID, err)
		}
	}
	return nil
}
