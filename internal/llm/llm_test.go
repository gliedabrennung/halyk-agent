package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantLimit bool
		wantAtLea time.Duration
	}{
		{
			name:      "nil error",
			err:       nil,
			wantLimit: false,
		},
		{
			name:      "malformed request is not worth retrying",
			err:       &APIError{Status: http.StatusBadRequest, Message: "invalid model name"},
			wantLimit: false,
		},
		{
			name:      "unauthorised is not worth retrying",
			err:       &APIError{Status: http.StatusUnauthorized, Message: "wrong api key provided"},
			wantLimit: false,
		},
		{
			name:      "server overloaded",
			err:       &APIError{Status: http.StatusServiceUnavailable, Message: "model is busy"},
			wantLimit: true,
			wantAtLea: 2 * time.Second,
		},
		{
			name:      "internal error",
			err:       &APIError{Status: http.StatusInternalServerError, Message: "internal"},
			wantLimit: true,
			wantAtLea: 2 * time.Second,
		},
		{
			name: "quota with stated delay",
			err: &APIError{
				Status:     http.StatusTooManyRequests,
				Code:       "request_quota_exceeded",
				Message:    "Requests per minute limit exceeded - too many requests sent.",
				RetryAfter: 49 * time.Second,
			},
			wantLimit: true,
			wantAtLea: 49 * time.Second,
		},
		{
			name:      "quota without a stated delay",
			err:       &APIError{Status: http.StatusTooManyRequests, Message: "too many requests"},
			wantLimit: true,
			wantAtLea: 30 * time.Second,
		},
		{
			name: "a rate limit already marked spent",
			err: &APIError{
				Status:    http.StatusTooManyRequests,
				Message:   "too many requests",
				Exhausted: true,
			},
			wantLimit: false,
		},
		{
			name:      "a dropped connection is worth another try",
			err:       &transportError{errors.New("EOF")},
			wantLimit: true,
			wantAtLea: 2 * time.Second,
		},
		{
			name:      "a cancelled context is not a provider failure",
			err:       context.Canceled,
			wantLimit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay, limited := isRetryable(tt.err, 1)
			if limited != tt.wantLimit {
				t.Fatalf("limited = %v, want %v", limited, tt.wantLimit)
			}
			if !limited {
				return
			}
			if delay < tt.wantAtLea {
				t.Errorf("delay = %v, want at least the requested %v", delay, tt.wantAtLea)
			}
		})
	}
}

func TestReserveSpacesRequests(t *testing.T) {
	c := &Client{shared: &shared{minInterval: 20 * time.Millisecond}}
	ctx := t.Context()

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := c.reserve(ctx); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}

	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Errorf("five reservations took %v, want at least ~80ms of spacing", elapsed)
	}
}

func TestReserveIsANoOpWithoutALimit(t *testing.T) {
	c := &Client{shared: &shared{minInterval: 0}}
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := c.reserve(t.Context()); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("unlimited client waited %v", elapsed)
	}
}

func TestBackoffDelaysTheWholeSchedule(t *testing.T) {
	c := &Client{shared: &shared{minInterval: time.Millisecond}}
	c.backoff(60 * time.Millisecond)

	start := time.Now()
	if err := c.reserve(t.Context()); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("reserve returned after %v, want it to respect the backoff", elapsed)
	}
}

func TestReserveHonoursContextCancellation(t *testing.T) {
	c := &Client{shared: &shared{minInterval: 10 * time.Second}}
	ctx, cancel := context.WithCancel(t.Context())

	if err := c.reserve(ctx); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	cancel()
	if err := c.reserve(ctx); err == nil {
		t.Error("a cancelled context must abort the wait instead of sleeping 10s")
	}
}
