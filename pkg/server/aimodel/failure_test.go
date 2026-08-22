package aimodel

import (
	"context"
	"errors"
	"testing"
)

// An oversized request and an exhausted balance both arrive as ordinary 4xx.
// Telling them apart from a malformed request is the difference between a turn
// that compacts and continues, a turn that ends with something to go and fix,
// and a turn that retries a rejection forever.
func TestClassifyStatusSeparatesTheFailuresThatNeedDifferentAnswers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		detail string
		want   CallFailure
	}{
		{"凭证被拒", 401, "invalid api key", CallAuth},
		{"限流", 429, "rate limit reached for requests", CallRateLimited},
		{"服务端错误", 503, "upstream unavailable", CallServer},
		{"请求本身不合法", 400, "unknown field 'temperture'", CallInvalidRequest},
		{
			"上下文溢出", 400,
			"This model's maximum context length is 262144 tokens, however you requested 300000",
			CallContextOverflow,
		},
		{
			"上下文溢出的另一种措辞", 400,
			"context_length_exceeded: prompt is too long for this model",
			CallContextOverflow,
		},
		{"请求体过大", 413, "payload too large", CallContextOverflow},
		{"额度用尽", 429, "You exceeded your current quota, please check your plan", CallQuota},
		{"余额不足", 402, "账户余额不足，请充值后重试", CallQuota},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyStatus(testCase.status, testCase.detail); got != testCase.want {
				t.Fatalf("classifyStatus(%d, %q) = %q, want %q",
					testCase.status, testCase.detail, got, testCase.want)
			}
		})
	}
}

// Retrying is only worth doing for a failure a second identical attempt could
// survive. A context overflow is deliberately outside that set: the same
// request overflows again, and it becomes retryable only after the caller has
// made the conversation smaller.
func TestOnlyTheFailuresASecondAttemptCouldSurviveAreRetryable(t *testing.T) {
	t.Parallel()
	retryable := []CallFailure{
		CallRateLimited, CallServer, CallTimeout, CallTransport, CallEmptyResponse,
	}
	terminal := []CallFailure{
		CallAuth, CallQuota, CallInvalidRequest, CallContextOverflow,
	}
	for _, kind := range retryable {
		if !IsRetryable(&CallError{Kind: kind}) {
			t.Fatalf("%q should be retryable", kind)
		}
	}
	for _, kind := range terminal {
		if IsRetryable(&CallError{Kind: kind}) {
			t.Fatalf("%q should not be retried", kind)
		}
	}
}

// The coarse sentinels stay meaningful for callers that only want to know
// whether an answer arrived at all.
func TestCallErrorKeepsTheCoarseSentinels(t *testing.T) {
	t.Parallel()
	if !errors.Is(&CallError{Kind: CallTimeout}, context.DeadlineExceeded) {
		t.Fatal("a timeout must still read as a deadline")
	}
	if !errors.Is(&CallError{Kind: CallServer}, ErrUnavailable) {
		t.Fatal("a server failure must still read as an unavailable endpoint")
	}
	if !IsContextOverflow(&CallError{Kind: CallContextOverflow}) {
		t.Fatal("the one failure a smaller request fixes must be recognizable")
	}
}
