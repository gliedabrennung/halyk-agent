package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestCallIntervalTakesTheTightestWindow(t *testing.T) {
	tests := []struct {
		name string
		rpm  int
		rph  int
		want time.Duration
	}{
		{name: "hourly window binds", rpm: 5, rph: 150, want: 24 * time.Second},
		{name: "minute window binds", rpm: 5, rph: 3600, want: 12 * time.Second},
		{name: "no hourly cap", rpm: 5, rph: 0, want: 12 * time.Second},
		{name: "no cap at all", rpm: 0, rph: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{RequestsPerMinute: tt.rpm, RequestsPerHour: tt.rph}
			if got := c.CallInterval(); got != tt.want {
				t.Errorf("CallInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestLoadFXRates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fx.yaml")
	body := "# a comment\n\nEUR: 1.16\n kzt : 0.0021 \n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rates, err := LoadFXRates(path)
	if err != nil {
		t.Fatalf("LoadFXRates: %v", err)
	}
	if got := rates["EUR"]; !got.Equal(decimal.RequireFromString("1.16")) {
		t.Errorf("EUR = %s, want 1.16", got)
	}
	if got := rates["KZT"]; !got.Equal(decimal.RequireFromString("0.0021")) {
		t.Errorf("KZT = %s, want 0.0021 (currency codes are upper-cased)", got)
	}
}

func TestLoadFXRatesMissingFile(t *testing.T) {
	rates, err := LoadFXRates(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing file should not be an error: %v", err)
	}
	if len(rates) != 0 {
		t.Errorf("rates = %v, want empty", rates)
	}
}

func TestLoadFXRatesRejectsBadValues(t *testing.T) {
	for _, body := range []string{"EUR: abc\n", "EUR: 0\n", "EUR: -1.16\n", "EUR 1.16\n"} {
		path := filepath.Join(t.TempDir(), "fx.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFXRates(path); err == nil {
			t.Errorf("%q should have been rejected", body)
		}
	}
}

func TestRealFXFileParses(t *testing.T) {
	rates, err := LoadFXRates("../../config/fx.yaml")
	if err != nil {
		t.Fatalf("the shipped config/fx.yaml must parse: %v", err)
	}
	if got := rates["EUR"]; !got.Equal(decimal.RequireFromString("1.16")) {
		t.Errorf("EUR = %s, want the auditor-disclosed 1.16", got)
	}
}
