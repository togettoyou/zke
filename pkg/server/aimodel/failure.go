package aimodel

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// CallFailure classifies why one model call failed.
//
// A classification rather than the endpoint's own sentence, for the same reason
// the trail records classifications everywhere else: the provider's text can
// carry an address, a header or a fragment of a credential, and the two things
// anything above this package actually needs to decide are whether to retry and
// whether the request has to get smaller first.
type CallFailure string

const (
	// CallTransport is a request that never reached an answer: DNS, TCP,
	// TLS, a connection reset mid-stream.
	CallTransport CallFailure = "transport"
	// CallTimeout is the configured request deadline elapsing.
	CallTimeout CallFailure = "timeout"
	// CallRateLimited is the endpoint asking for less traffic.
	CallRateLimited CallFailure = "rate_limited"
	// CallServer is a 5xx: the endpoint's problem, and usually a passing one.
	CallServer CallFailure = "server"
	// CallAuth is a credential the endpoint refused. Retrying sends the same
	// wrong credential again, so it is terminal.
	CallAuth CallFailure = "auth"
	// CallQuota is an exhausted account balance rather than a transient
	// rate limit. Also terminal: waiting does not add credit.
	CallQuota CallFailure = "quota"
	// CallInvalidRequest is a request the endpoint would not accept. It
	// fails identically every time.
	CallInvalidRequest CallFailure = "invalid_request"
	// CallContextOverflow is a request the endpoint rejected for exceeding
	// the model's context window. Not retryable as sent, but retryable once the
	// conversation has been compacted, which is what makes it worth separating
	// from every other rejected request.
	CallContextOverflow CallFailure = "context_overflow"
	// CallEmptyResponse is a call that completed normally and produced
	// nothing at all: no text and no tool call. The attempt left nothing
	// durable behind, so repeating it is safe.
	CallEmptyResponse CallFailure = "empty_response"
)

// CallError is one classified model call failure.
type CallError struct {
	Kind CallFailure
	// Status is the HTTP status that produced the classification, when there
	// was one. Diagnostic only: nothing routes on it.
	Status int
}

func (err *CallError) Error() string {
	return "AI model call failed: " + string(err.Kind)
}

// Unwrap keeps the coarse sentinels meaningful. Everything that is not a
// deadline is still an unavailable endpoint from the perspective of a caller
// that only wants to know whether it got an answer.
func (err *CallError) Unwrap() error {
	if err.Kind == CallTimeout {
		return context.DeadlineExceeded
	}
	return ErrUnavailable
}

// Retryable reports whether sending the identical request again could succeed.
//
// Context overflow is deliberately excluded: the same request overflows again.
// It becomes retryable only after the caller has made the conversation smaller,
// which is a decision the runtime makes rather than a property of the failure.
func (err *CallError) Retryable() bool {
	switch err.Kind {
	case CallRateLimited, CallServer, CallTimeout,
		CallTransport, CallEmptyResponse:
		return true
	default:
		return false
	}
}

// FailureOf reports how a model call failed. An error this package did not
// classify is treated as a transport failure, which is the conservative reading
// for something that came back from a network call.
func FailureOf(err error) CallFailure {
	var call *CallError
	if errors.As(err, &call) {
		return call.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CallTimeout
	}
	return CallTransport
}

// IsRetryable reports whether the identical request is worth sending again.
func IsRetryable(err error) bool {
	var call *CallError
	if errors.As(err, &call) {
		return call.Retryable()
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// IsContextOverflow reports the one failure a smaller request would fix.
func IsContextOverflow(err error) bool {
	return FailureOf(err) == CallContextOverflow
}

// wireError is the error envelope every OpenAI-compatible endpoint agrees on.
// Only its classification fields are read; the message is matched against and
// then discarded.
type wireError struct {
	Error struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// classifyStatus maps one non-2xx response onto a failure kind.
//
// The status alone is not enough for the two cases that matter most: an
// exhausted balance and an oversized request both arrive as ordinary 4xx, and
// telling them apart from a malformed request is the difference between a turn
// that ends and a turn that compacts and continues. Both are recognized from
// the endpoint's own wording, because no OpenAI-compatible service reports
// either through a dedicated status.
func classifyStatus(status int, detail string) CallFailure {
	switch {
	case status == 401 || status == 403:
		return CallAuth
	case isQuotaExhausted(detail):
		return CallQuota
	case status == 429:
		return CallRateLimited
	case status == 413:
		return CallContextOverflow
	case status == 400 || status == 422:
		if isContextOverflow(detail) {
			return CallContextOverflow
		}
		return CallInvalidRequest
	case status >= 500:
		return CallServer
	default:
		return CallInvalidRequest
	}
}

var (
	contextOverflowPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)context[ _-](length|window)[ _-]?(exceeded|overflow|limit)`),
		regexp.MustCompile(`(?i)(maximum|max)[ _-]?(allowed |supported )?context[ _-](length|window)`),
		regexp.MustCompile(`(?i)(request|prompt|input|messages?)[^.]{0,40}too (large|long) for[^.]{0,40}(model|context)`),
		regexp.MustCompile(`(?i)(input|prompt|request|messages?)[^.]{0,40}(exceeds?|exceeded|larger than)[^.]{0,40}context`),
		regexp.MustCompile(`(?i)(tokens?|长度|输入)[^。]{0,20}(超(出|过))[^。]{0,20}(上下文|context)`),
	}
	quotaPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)insufficient[ _-]+(quota|balance|credits?)`),
		regexp.MustCompile(`(?i)(quota|usage[ _-]limit)[ _-]+(exceeded|exhausted|reached)`),
		regexp.MustCompile(`(?i)exceeded[ _-]+(your |the )?(current )?quota`),
		regexp.MustCompile(`(?i)(balance|credits?)[ _-]+(exhausted|depleted)`),
		regexp.MustCompile(`(?i)out[ _-]+of[ _-]+(credits?|budget)`),
		regexp.MustCompile(`余额不足`),
	}
)

func isContextOverflow(detail string) bool { return matchesAny(detail, contextOverflowPatterns) }

func isQuotaExhausted(detail string) bool { return matchesAny(detail, quotaPatterns) }

func matchesAny(detail string, patterns []*regexp.Regexp) bool {
	if strings.TrimSpace(detail) == "" {
		return false
	}
	for _, pattern := range patterns {
		if pattern.MatchString(detail) {
			return true
		}
	}
	return false
}
