package llm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

const _quotaBody = `{"error":{"code":429,"message":"Quota exceeded for quota metric ` +
	`'Generate requests per day'","status":"RESOURCE_EXHAUSTED"}}`

func testClient(t *testing.T, baseURL string) (*Client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		APIKey: "test-key", BaseURL: baseURL, Model: "test-model",
		MaxConcurrency: 2, LogDir: t.TempDir(),
	}
	return New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func TestDailyQuotaClosesTheNetworkForTheRestOfTheRun(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, _quotaBody)
	}))
	defer srv.Close()

	c, _ := testClient(t, srv.URL)
	ctx := context.Background()

	if _, err := c.Complete(ctx, Request{Name: "probe", Prompt: "one"}); err == nil {
		t.Fatal("a daily-quota 429 must be an error")
	}
	if err := c.QuotaExhausted(); err == nil {
		t.Fatal("the client must remember that the daily quota is gone")
	}

	_, err := c.Complete(ctx, Request{Name: "probe", Prompt: "two"})
	if err == nil {
		t.Fatal("calls after exhaustion must fail")
	}
	if !IsQuotaExhausted(err) {
		t.Errorf("the reason must survive the wrapping, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the provider was called %d times; only the first call may reach the network", n)
	}
}

func TestCacheIsStillServedAfterQuotaIsGone(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"cached answer"}}]}`)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, _quotaBody)
	}))
	defer srv.Close()

	c, _ := testClient(t, srv.URL)
	ctx := context.Background()
	warm := Request{Name: "probe", Prompt: "warm"}

	if _, err := c.Complete(ctx, warm); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.Complete(ctx, Request{Name: "probe", Prompt: "cold"}); err == nil {
		t.Fatal("the second call must hit the quota wall")
	}

	got, err := c.Complete(ctx, warm)
	if err != nil {
		t.Fatalf("an exhausted quota must not block a cached answer: %v", err)
	}
	if got != "cached answer" {
		t.Errorf("got %q, want the cached answer", got)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the provider was called %d times, want 2 (warm-up + the one that hit the wall)", n)
	}
}
