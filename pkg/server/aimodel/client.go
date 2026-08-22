package aimodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// Failure is the classification of a probe that did not succeed.
//
// The set is small on purpose: each value names a different thing for the
// operator to go and fix — the credential, the address, the model name, the
// network — and a finer split would be a distinction nobody could act on.
type Failure string

const (
	FailureUnauthorized       Failure = "unauthorized"
	FailureModelNotFound      Failure = "model_not_found"
	FailureUnreachable        Failure = "unreachable"
	FailureTimeout            Failure = "timeout"
	FailureUnexpectedResponse Failure = "unexpected_response"
)

// Outcome is one connectivity test's result.
//
// Detail is written here rather than taken from the endpoint's response body.
// That body is text from outside ZKE: it may quote the request — credential
// included — and it is rendered in the Console, where untrusted text does not
// belong. The status code is enough of a raw fact to make the classification
// checkable.
type Outcome struct {
	Succeeded bool
	Failure   Failure
	Detail    string
	// Status is the HTTP status the endpoint returned, or 0 when no response
	// arrived.
	Status int
}

// probeMaxResponseBytes bounds what a probe reads. A misconfigured address can
// point at something that streams indefinitely, and this request exists only to
// find out whether the far side answers like the selected model protocol.
const probeMaxResponseBytes = 32 * 1024

const (
	chatCompletionsPath = "/chat/completions"
	responsesPath       = "/responses"
)

// HTTPProber calls an OpenAI-compatible endpoint over HTTP.
//
// Redirects are refused. The request carries the API Key in an Authorization
// header, and following a redirect would hand that header — or, if Go strips it
// across hosts, a confusing unauthenticated retry — to a host the operator
// never configured. A redirecting endpoint is a configuration to correct, not
// one to silently follow.
type HTTPProber struct {
	client *http.Client
}

func NewHTTPProber() *HTTPProber {
	return &HTTPProber{
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type chatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionResponse is the smallest shape that distinguishes a chat
// completions endpoint from anything else answering 200 at that address — a
// reverse proxy's index page, say, or an unrelated API.
type chatCompletionResponse struct {
	Choices []struct {
		Index int `json:"index"`
	} `json:"choices"`
}

type responsesRequest struct {
	Model           string `json:"model"`
	Input           string `json:"input"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Stream          bool   `json:"stream"`
}

type responsesResponse struct {
	Output []json.RawMessage `json:"output"`
}

// Probe sends one minimal request in the selected protocol and classifies the result.
func (prober *HTTPProber) Probe(ctx context.Context, target Target) Outcome {
	var payload any
	var operationPath string
	switch target.APIProtocol {
	case APIProtocolResponses:
		payload = responsesRequest{
			Model: target.Model, Input: "ping", MaxOutputTokens: 16, Stream: false,
		}
		operationPath = responsesPath
	case APIProtocolChatCompletions:
		payload = chatCompletionRequest{
			Model: target.Model,
			Messages: []chatMessage{{
				Role: "user", Content: "ping",
			}},
			MaxTokens: 1,
			Stream:    false,
		}
		operationPath = chatCompletionsPath
	default:
		return Outcome{Failure: FailureUnexpectedResponse, Detail: "模型端点使用了不支持的 API 协议"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Outcome{Failure: FailureUnexpectedResponse, Detail: "无法构造测试请求"}
	}

	timeout := target.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		target.BaseURL+operationPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return Outcome{
			Failure: FailureUnreachable,
			Detail:  "接入地址无法构成有效请求，请检查地址格式",
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if target.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+target.APIKey)
	}

	response, err := prober.client.Do(request)
	if err != nil {
		return outcomeFromTransportError(requestCtx, ctx, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, probeMaxResponseBytes))
		_ = response.Body.Close()
	}()
	return outcomeFromResponse(response, target.APIProtocol)
}

// outcomeFromTransportError separates the deadline this probe imposed from
// every other reason the request never completed. The caller's own context
// ending is not the endpoint's fault and is reported as a cancellation of the
// enclosing request instead.
func outcomeFromTransportError(requestCtx, callerCtx context.Context, err error) Outcome {
	var timeoutError net.Error
	switch {
	case callerCtx.Err() != nil:
		return Outcome{
			Failure: FailureTimeout,
			Detail:  "测试请求在完成前被取消",
		}
	case errors.Is(requestCtx.Err(), context.DeadlineExceeded),
		// A transport-level deadline — a dial or a TLS handshake that ran out —
		// is the same answer for the operator even though this probe's own
		// context is still live.
		errors.As(err, &timeoutError) && timeoutError.Timeout():
		return Outcome{
			Failure: FailureTimeout,
			Detail:  "模型端点在配置的请求超时内没有响应",
		}
	default:
		return Outcome{
			Failure: FailureUnreachable,
			Detail:  "无法连接到模型端点，请检查接入地址、网络与证书",
		}
	}
}

func outcomeFromResponse(response *http.Response, protocol APIProtocol) Outcome {
	status := response.StatusCode
	switch {
	case status == http.StatusOK:
		return outcomeFromBody(response, status, protocol)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Outcome{
			Failure: FailureUnauthorized,
			Detail:  "模型端点拒绝了这份凭证，请检查 API Key",
			Status:  status,
		}
	case status == http.StatusNotFound:
		// A 404 is ambiguous between "no such model" and "no such path", and
		// the endpoint's own wording is not reliable enough to tell them apart.
		// Naming both is more useful than picking one and being wrong half the
		// time.
		return Outcome{
			Failure: FailureModelNotFound,
			Detail:  "模型端点返回 404：模型名不存在，或接入地址的路径不正确",
			Status:  status,
		}
	case status >= 400 && status < 500:
		return Outcome{
			Failure: FailureUnexpectedResponse,
			Detail:  "模型端点拒绝了这次请求，请检查模型名与接入地址",
			Status:  status,
		}
	default:
		return Outcome{
			Failure: FailureUnexpectedResponse,
			Detail:  "模型端点返回了意料之外的状态码，请检查该服务自身的状态与接入地址",
			Status:  status,
		}
	}
}

// outcomeFromBody decides whether a 200 matches the selected protocol. An
// unrelated body is a misconfiguration worth catching before the first run.
func outcomeFromBody(response *http.Response, status int, protocol APIProtocol) Outcome {
	payload, err := io.ReadAll(io.LimitReader(response.Body, probeMaxResponseBytes))
	if err != nil {
		return Outcome{
			Failure: FailureUnexpectedResponse,
			Detail:  "读取模型端点响应失败",
			Status:  status,
		}
	}
	valid := false
	switch protocol {
	case APIProtocolResponses:
		var result responsesResponse
		valid = json.Unmarshal(payload, &result) == nil && result.Output != nil
	case APIProtocolChatCompletions:
		var result chatCompletionResponse
		valid = json.Unmarshal(payload, &result) == nil && result.Choices != nil
	}
	if !valid {
		return Outcome{
			Failure: FailureUnexpectedResponse,
			Detail:  "接入地址返回的响应与所选 API 协议不匹配，请检查协议与 /v1 一类的 API 前缀",
			Status:  status,
		}
	}
	return Outcome{Succeeded: true, Status: status}
}
