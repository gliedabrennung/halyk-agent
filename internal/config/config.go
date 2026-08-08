package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Config struct {
	Team         string
	ContactEmail string

	DataDir      string
	OutDir       string
	ArtifactsDir string
	CacheDir     string
	LogDir       string
	DBPath       string

	APIKey  string
	BaseURL string
	Model   string

	ReasoningEffort string

	Seed *int

	MaxConcurrency int

	RequestsPerMinute int
	RequestsPerHour   int

	LogLevel string
}

func (c *Config) LedgerPath() string   { return filepath.Join(c.DataDir, "master_ledger_2025.csv") }
func (c *Config) TemplatePath() string { return filepath.Join(c.DataDir, "submission_template.json") }
func (c *Config) DocumentsDir() string { return filepath.Join(c.DataDir, "documents") }

func (c *Config) GroundTruthPath() string { return filepath.Join(c.DataDir, "ground_truth.json") }

func (c *Config) SubmissionPath() string { return filepath.Join(c.OutDir, "submission.json") }

func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, line, text)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}

func Load() (*Config, error) {
	if err := LoadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	c := &Config{
		Team:              env("TEAM", "halyk-agent"),
		ContactEmail:      env("CONTACT_EMAIL", ""),
		DataDir:           env("DATA_DIR", "./data"),
		OutDir:            env("OUT_DIR", "./out"),
		ArtifactsDir:      env("ARTIFACTS_DIR", "./artifacts"),
		CacheDir:          env("CACHE_DIR", "./.cache"),
		LogDir:            env("LOG_DIR", "./logs"),
		DBPath:            env("DB", "./.cache/halyk.db"),
		APIKey:            env("LLM_API_KEY", os.Getenv("GOOGLE_API_KEY")),
		BaseURL:           env("LLM_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
		Model:             env("MODEL", "gemini-3.5-flash-lite"),
		ReasoningEffort:   env("REASONING_EFFORT", ""),
		MaxConcurrency:    envInt("MAX_CONCURRENCY", 4),
		RequestsPerMinute: envInt("RPM", 14),
		RequestsPerHour:   envInt("RPH", 0),
		LogLevel:          env("LOG_LEVEL", "info"),
	}
	if err := validReasoningEffort(c.ReasoningEffort); err != nil {
		return nil, err
	}
	seed, err := parseSeed(env("LLM_SEED", "none"))
	if err != nil {
		return nil, err
	}
	c.Seed = seed

	absolute := []*string{&c.DataDir, &c.OutDir, &c.ArtifactsDir, &c.CacheDir, &c.LogDir, &c.DBPath}
	for _, p := range absolute {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", *p, err)
		}
		*p = abs
	}
	return c, nil
}

func (c *Config) FXPath() string { return filepath.Join("config", "fx.yaml") }

func LoadFXRates(path string) (map[string]decimal.Decimal, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]decimal.Decimal), nil
		}
		return nil, err
	}
	defer f.Close()

	out := make(map[string]decimal.Decimal)
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected \"CURRENCY: rate\", got %q", path, line, text)
		}
		cur := strings.ToUpper(strings.TrimSpace(key))
		rate, err := decimal.NewFromString(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: rate for %s is not a decimal: %q", path, line, cur, value)
		}
		if !rate.IsPositive() {
			return nil, fmt.Errorf("%s:%d: rate for %s must be positive, got %s", path, line, cur, rate)
		}
		out[cur] = rate
	}
	return out, sc.Err()
}

func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.OutDir, c.ArtifactsDir, c.CacheDir, c.LogDir,
		filepath.Dir(c.DBPath), filepath.Join(c.LogDir, "llm"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

func (c *Config) CheckDataset() error {
	var missing []string
	for _, p := range []string{c.LedgerPath(), c.TemplatePath(), c.DocumentsDir()} {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("dataset incomplete under %s, missing: %s (set DATA_DIR)",
			c.DataDir, strings.Join(missing, ", "))
	}
	return nil
}

func (c *Config) RequireAPIKey() error {
	if c.APIKey == "" {
		return fmt.Errorf("LLM_API_KEY is not set (GOOGLE_API_KEY also works); required for LLM stages")
	}
	return nil
}

func (c *Config) CallInterval() time.Duration {
	interval := time.Duration(0)
	if c.RequestsPerMinute > 0 {
		interval = time.Minute / time.Duration(c.RequestsPerMinute)
	}
	if c.RequestsPerHour > 0 {
		if perHour := time.Hour / time.Duration(c.RequestsPerHour); perHour > interval {
			interval = perHour
		}
	}
	return interval
}

func parseSeed(v string) (*int, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "none") || strings.EqualFold(v, "off") {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf(`LLM_SEED is %q, want an integer or "none"`, v)
	}
	return &n, nil
}

func validReasoningEffort(v string) error {
	switch v {
	case "", "low", "medium", "high":
		return nil
	}
	return fmt.Errorf("REASONING_EFFORT is %q, want low, medium, high, or empty", v)
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
