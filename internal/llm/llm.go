package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gliedabrennung/halyk-agent/internal/config"
	"github.com/gliedabrennung/halyk-agent/internal/store"
)

type Client struct {
	cfg *config.Config
	st  *store.Store
	log *slog.Logger

	nonce string

	shared *shared
}

type shared struct {
	sem chan struct{}

	rateMu      sync.Mutex
	nextSlot    time.Time
	minInterval time.Duration

	quotaMu  sync.Mutex
	quotaErr error

	http *http.Client
}

func (s *shared) quotaSpent() error {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	return s.quotaErr
}

func (s *shared) spendQuota(err error) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if s.quotaErr == nil {
		s.quotaErr = err
	}
}

// QuotaExhausted говорит, упёрся ли этот клиент в суточную квоту провайдера. Стадия может
// на этом остановиться вместо того, чтобы получать одну и ту же ошибку на каждом элементе.
func (c *Client) QuotaExhausted() error {
	if err := c.shared.quotaSpent(); err != nil {
		return fmt.Errorf("the model's daily quota is exhausted: %w", err)
	}
	return nil
}

func NewWithNonce(cfg *config.Config, st *store.Store, log *slog.Logger, nonce string) *Client {
	c := New(cfg, st, log)
	c.nonce = nonce
	return c
}

func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Client {
	return &Client{
		cfg: cfg,
		st:  st,
		log: log,
		shared: &shared{
			sem:         make(chan struct{}, cfg.MaxConcurrency),
			minInterval: cfg.CallInterval(),
			http:        &http.Client{Timeout: cfg.RequestTimeout},
		},
	}
}

func (c *Client) WithLogger(log *slog.Logger) *Client {
	if log == nil {
		return c
	}
	out := *c
	out.log = log
	return &out
}

func (c *Client) reserve(ctx context.Context) error {
	s := c.shared
	if s.minInterval <= 0 {
		return nil
	}
	s.rateMu.Lock()
	now := time.Now()
	if s.nextSlot.Before(now) {
		s.nextSlot = now
	}
	wait := s.nextSlot.Sub(now)
	s.nextSlot = s.nextSlot.Add(s.minInterval)
	s.rateMu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) backoff(d time.Duration) {
	s := c.shared
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if until := time.Now().Add(d); s.nextSlot.Before(until) {
		s.nextSlot = until
	}
}

type APIError struct {
	Status     int
	Code       string
	Type       string
	Message    string
	RetryAfter time.Duration

	Exhausted bool
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, msg)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

var _exhaustedWindows = []string{
	"per day", "per-day", "perday", "daily", "requests_per_day", "free_tier_requests",
}

func IsQuotaExhausted(err error) bool {
	const longWait = 20 * time.Minute
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		return false
	}
	if apiErr.Exhausted || apiErr.RetryAfter > longWait {
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return slices.ContainsFunc(_exhaustedWindows, func(w string) bool { return strings.Contains(msg, w) })
}

func isRetryable(err error, attempt int) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	growing := time.Duration(1<<uint(min(attempt, 5))) * time.Second

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusTooManyRequests:
			if IsQuotaExhausted(err) {
				return 0, false
			}
			if apiErr.RetryAfter > 0 {
				return apiErr.RetryAfter + time.Second, true
			}
			return 30 * time.Second, true
		case apiErr.Status == http.StatusRequestTimeout,
			apiErr.Status == http.StatusConflict,
			apiErr.Status >= 500:
			return growing, true
		default:
			return 0, false
		}
	}

	var netErr *transportError
	if errors.As(err, &netErr) {
		return growing, true
	}
	return 0, false
}

type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

type Request struct {
	Model string

	Name string

	Instruction string

	Prompt string

	SchemaVersion string

	JSON bool

	Temperature float32

	Description string
}

func (r *Request) modelName(cfg *config.Config) string {
	if r.Model != "" {
		return r.Model
	}
	return cfg.Model
}

func (r *Request) cacheKey(modelName string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%v\x00%g", r.Name, r.Instruction, r.Prompt, r.JSON, r.Temperature)
	return store.CacheKey(modelName, hex.EncodeToString(h.Sum(nil)), r.SchemaVersion)
}

func (c *Client) Complete(ctx context.Context, req Request) (string, error) {
	const maxRetries = 6
	modelName := req.modelName(c.cfg)
	key := req.cacheKey(modelName + c.nonce)

	if resp, ok, err := c.st.GetCached(key); err != nil {
		return "", fmt.Errorf("cache lookup: %w", err)
	} else if ok {
		c.log.Debug("llm cache hit", "agent", req.Name, "model", modelName, "key", key[:12])
		return resp, nil
	}

	if err := c.cfg.RequireAPIKey(); err != nil {
		return "", err
	}
	if err := c.QuotaExhausted(); err != nil {
		return "", fmt.Errorf("llm call %s (%s): %w", req.Name, modelName, err)
	}

	select {
	case c.shared.sem <- struct{}{}:
		defer func() { <-c.shared.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	const rateLimitBudget = 2 * time.Minute

	var (
		out           completion
		err           error
		latency       time.Duration
		waitedOnLimit time.Duration
	)
	for attempt := 1; ; attempt++ {
		if err = c.reserve(ctx); err != nil {
			return "", err
		}
		start := time.Now()
		out, err = c.call(ctx, modelName, req)
		latency = time.Since(start)
		if err == nil {
			break
		}
		delay, retryable := isRetryable(err, attempt)

		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
			waitedOnLimit += delay
			if waitedOnLimit >= rateLimitBudget {
				apiErr.Exhausted = true
				retryable = false
			}
		}

		if !retryable || attempt > maxRetries {
			if IsQuotaExhausted(err) {
				c.shared.spendQuota(err)
				c.log.Error("daily quota exhausted; the remaining calls of this run are skipped",
					"agent", req.Name, "model", modelName)
			}
			return "", fmt.Errorf("llm call %s (%s): %w", req.Name, modelName, err)
		}
		c.backoff(delay)
		c.log.Warn("call failed, retrying",
			"agent", req.Name, "model", modelName, "attempt", attempt, "wait", delay.Round(time.Second), "err", err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	if err := c.st.PutCached(key, modelName, out.Text, out.TokensIn, out.TokensOut, latency); err != nil {
		c.log.Warn("cache write failed", "err", err, "key", key[:12])
	}
	if err := store.DumpLLMCall(c.cfg.LogDir, store.LLMCallLog{
		Key: key, Model: modelName,
		Prompt:    req.Instruction + "\n---\n" + req.Prompt,
		Response:  out.Text,
		Reasoning: out.Reasoning,
		TokensIn:  out.TokensIn,
		TokensOut: out.TokensOut,
		LatencyMS: latency.Milliseconds(),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		c.log.Warn("llm dump failed", "err", err, "key", key[:12])
	}

	c.log.Info("llm call", "agent", req.Name, "model", modelName,
		"tokens_in", out.TokensIn, "tokens_out", out.TokensOut, "reasoning_tokens", out.ReasoningTokens,
		"ms", latency.Milliseconds())
	return out.Text, nil
}

type completion struct {
	Text            string
	Reasoning       string
	TokensIn        int
	TokensOut       int
	ReasoningTokens int
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Temperature         float32         `json:"temperature"`
	Seed                *int            `json:"seed,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	Stream              bool            `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (c *Client) call(ctx context.Context, modelName string, req Request) (completion, error) {
	const maxCompletionTokens = 16384
	body := chatRequest{
		Model:               modelName,
		Temperature:         req.Temperature,
		Seed:                c.cfg.Seed,
		MaxCompletionTokens: maxCompletionTokens,
		ReasoningEffort:     c.cfg.ReasoningEffort,
	}
	if req.Instruction != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: req.Instruction})
	}
	body.Messages = append(body.Messages, chatMessage{Role: "user", Content: req.Prompt})
	if req.JSON {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return completion{}, fmt.Errorf("encode request: %w", err)
	}

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return completion{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.shared.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return completion{}, ctx.Err()
		}
		return completion{}, &transportError{err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return completion{}, &transportError{err}
	}
	c.reportQuota(resp.Header)

	if resp.StatusCode != http.StatusOK {
		return completion{}, parseAPIError(resp, raw)
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return completion{}, fmt.Errorf("decode response: %w: %s", err, snippet(raw))
	}
	if len(parsed.Choices) == 0 {
		return completion{}, errors.New("model returned no choices")
	}
	choice := parsed.Choices[0]
	out := completion{
		Text:            strings.TrimSpace(choice.Message.Content),
		Reasoning:       choice.Message.Reasoning,
		TokensIn:        parsed.Usage.PromptTokens,
		TokensOut:       parsed.Usage.CompletionTokens,
		ReasoningTokens: parsed.Usage.CompletionTokensDetails.ReasoningTokens,
	}
	if choice.FinishReason == "length" {
		return out, fmt.Errorf("answer hit the %d-token ceiling and is truncated; shorten the prompt or lower REASONING_EFFORT", maxCompletionTokens)
	}
	if out.Text == "" {
		return out, fmt.Errorf("model returned no text output (finish_reason %q)", choice.FinishReason)
	}
	return out, nil
}

func (c *Client) reportQuota(h http.Header) {
	remaining := func(window string) (int, bool) {
		v := h.Get("x-ratelimit-remaining-" + window)
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		return n, err == nil
	}

	reqDay, okReq := remaining("requests-day")
	tokDay, okTok := remaining("tokens-day")
	reqHour, _ := remaining("requests-hour")
	if !okReq && !okTok {
		return
	}
	c.log.Debug("provider quota left",
		"requests_hour", reqHour, "requests_day", reqDay, "tokens_day", tokDay)

	if okReq && reqDay > 0 && reqDay <= 50 {
		c.log.Warn("running out of the daily request allowance", "requests_left_today", reqDay)
	}
	if okTok && tokDay > 0 && tokDay <= 50_000 {
		c.log.Warn("running out of the daily token allowance", "tokens_left_today", tokDay)
	}
}

var _retryDelayRe = regexp.MustCompile(`retryDelay:\s*"?(\d+(?:\.\d+)?)s`)

type errorBody struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Status  string          `json:"status"`
	Error   *errorBody      `json:"error"`
}

func parseAPIError(resp *http.Response, raw []byte) error {
	apiErr := &APIError{Status: resp.StatusCode, Message: snippet(raw)}

	payload := bytes.TrimSpace(raw)
	if len(payload) > 0 && payload[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(payload, &arr) == nil && len(arr) > 0 {
			payload = arr[0]
		}
	}

	var body errorBody
	if json.Unmarshal(payload, &body) == nil {
		if body.Message == "" && body.Error != nil {
			body = *body.Error
		}
		if body.Message != "" {
			apiErr.Message = body.Message
			apiErr.Type = body.Type
			apiErr.Code = strings.Trim(string(body.Code), `"`)
			if apiErr.Code == "" {
				apiErr.Code = body.Status
			}
		}
	}
	if m := _retryDelayRe.FindStringSubmatch(string(raw)); m != nil {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil {
			apiErr.RetryAfter = time.Duration(secs * float64(time.Second))
		}
	}
	if v := resp.Header.Get("retry-after"); v != "" {
		if secs, err := time.ParseDuration(v + "s"); err == nil {
			apiErr.RetryAfter = secs
		}
	}
	return apiErr
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
