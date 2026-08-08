package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDailyQuotaIsNotRetried(t *testing.T) {
	daily := &APIError{
		Status:     http.StatusTooManyRequests,
		Code:       "request_quota_exceeded",
		Message:    "Requests per day limit exceeded - too many requests sent.",
		RetryAfter: 3 * time.Hour,
	}
	if _, retry := isRetryable(daily, 1); retry {
		t.Error("an exhausted daily allowance must not be retried")
	}
	if !IsQuotaExhausted(daily) {
		t.Error("the caller must be able to recognise an exhausted daily allowance")
	}

	perMinute := &APIError{
		Status:     http.StatusTooManyRequests,
		Code:       "request_quota_exceeded",
		Message:    "Requests per minute limit exceeded - too many requests sent.",
		RetryAfter: 54 * time.Second,
	}
	delay, retry := isRetryable(perMinute, 1)
	if !retry {
		t.Fatal("a per-minute rate limit must be retried")
	}
	if delay < 54*time.Second {
		t.Errorf("delay = %s, want at least the 54s the API asked for", delay)
	}
	if IsQuotaExhausted(perMinute) {
		t.Error("a per-minute limit is not an exhausted allowance")
	}
}

func TestAVeryLongRetryAfterCountsAsExhausted(t *testing.T) {
	hourly := &APIError{
		Status:     http.StatusTooManyRequests,
		Code:       "request_quota_exceeded",
		Message:    "Requests per hour limit exceeded - too many requests sent.",
		RetryAfter: 42 * time.Minute,
	}
	if !IsQuotaExhausted(hourly) {
		t.Error("a 42-minute wait is not something a stage should sleep through")
	}
	if _, retry := isRetryable(hourly, 1); retry {
		t.Error("a 42-minute wait must not be retried in place")
	}
}

func TestTransientErrorsBackOff(t *testing.T) {
	err := &APIError{Status: http.StatusServiceUnavailable, Message: "the model is overloaded"}
	first, retry := isRetryable(err, 1)
	if !retry {
		t.Fatal("a 503 must be retried")
	}
	later, _ := isRetryable(err, 4)
	if later <= first {
		t.Errorf("backoff must grow: attempt 1 %s, attempt 4 %s", first, later)
	}
}

func TestUnrelatedErrorsAreNotRetried(t *testing.T) {
	if _, retry := isRetryable(&APIError{Status: http.StatusBadRequest, Message: "invalid model name"}, 1); retry {
		t.Error("a permanent error must not be retried")
	}
	if _, retry := isRetryable(nil, 1); retry {
		t.Error("no error means nothing to retry")
	}
}

func TestGoogleQuotaShapeIsUnderstood(t *testing.T) {
	const body = `{"error":{"code":429,"message":"You exceeded your current quota. ` +
		`Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, ` +
		`quotaId:GenerateRequestsPerDayPerProjectPerModel-FreeTier, retryDelay:50s",` +
		`"status":"RESOURCE_EXHAUSTED"}}`

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	apiErr, ok := parseAPIError(resp, []byte(body)).(*APIError)
	if !ok {
		t.Fatal("parseAPIError must return *APIError")
	}
	if apiErr.Code != "429" {
		t.Errorf("code = %q, want 429 decoded from a JSON number", apiErr.Code)
	}
	if apiErr.RetryAfter != 50*time.Second {
		t.Errorf("retryAfter = %s, want the 50s stated in the body", apiErr.RetryAfter)
	}
	if !IsQuotaExhausted(apiErr) {
		t.Error("GenerateRequestsPerDayPerProjectPerModel is the daily window and must count as exhausted")
	}
	if _, retry := isRetryable(apiErr, 1); retry {
		t.Error("a spent daily allowance must not be retried")
	}
}

func TestGooglePerMinuteQuotaIsRetried(t *testing.T) {
	const body = `{"error":{"code":429,"message":"Quota exceeded, ` +
		`quotaId:GenerateRequestsPerMinutePerProjectPerModel-FreeTier, retryDelay:12s",` +
		`"status":"RESOURCE_EXHAUSTED"}}`

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	err := parseAPIError(resp, []byte(body))
	if IsQuotaExhausted(err) {
		t.Error("a per-minute limit is not an exhausted allowance")
	}
	delay, retry := isRetryable(err, 1)
	if !retry {
		t.Fatal("a per-minute rate limit must be retried")
	}
	if delay < 12*time.Second {
		t.Errorf("delay = %s, want at least the 12s the API asked for", delay)
	}
}

func TestParseAPIErrorReadsTheProviderShape(t *testing.T) {
	const body = `{"message":"Requests per minute limit exceeded - too many requests sent.",` +
		`"type":"too_many_requests_error","param":"quota","code":"request_quota_exceeded"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("retry-after", "54")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	apiErr, ok := parseAPIError(resp, []byte(body)).(*APIError)
	if !ok {
		t.Fatal("parseAPIError must return *APIError")
	}
	if apiErr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.Status)
	}
	if apiErr.Code != "request_quota_exceeded" {
		t.Errorf("code = %q", apiErr.Code)
	}
	if apiErr.RetryAfter != 54*time.Second {
		t.Errorf("retry-after = %s, want 54s", apiErr.RetryAfter)
	}
}

func TestGoogleFreeTierMetricCountsAsExhausted(t *testing.T) {
	const body = `{"error":{"code":429,"message":"You exceeded your current quota, please check your ` +
		`plan and billing details. * Quota exceeded for metric: ` +
		`generativelanguage.googleapis.com/generate_content_free_tier_requests",` +
		`"status":"RESOURCE_EXHAUSTED"}}`

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	err := parseAPIError(resp, []byte(body))

	if !IsQuotaExhausted(err) {
		t.Error("the free-tier request metric names a spent allowance, not a per-minute pause")
	}
	if _, retry := isRetryable(err, 1); retry {
		t.Error("a spent allowance must not be retried")
	}
}

func TestExhaustedFlagSurvivesTheMarkerList(t *testing.T) {
	silent := &APIError{
		Status:  http.StatusTooManyRequests,
		Message: "429 too many requests",
	}
	if IsQuotaExhausted(silent) {
		t.Fatal("a bare rate limit is not an exhausted allowance")
	}

	silent.Exhausted = true
	if !IsQuotaExhausted(silent) {
		t.Error("the caller must be able to mark an allowance spent without help from the wording")
	}
	if _, retry := isRetryable(silent, 1); retry {
		t.Error("an allowance marked spent must not be retried")
	}
}
