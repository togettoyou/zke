package aimodel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// completeAgainst runs one completion against a stub endpoint and hands back
// both the assembled result and the request the endpoint received, because half
// of what matters here is what went out.
func completeAgainst(
	t *testing.T,
	protocol APIProtocol,
	handler http.HandlerFunc,
	input CompletionInput,
) (Completion, map[string]any, error) {
	t.Helper()
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(payload, &sent)
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	completion, err := NewHTTPProber().Complete(context.Background(), Target{
		BaseURL:         server.URL + "/v1",
		Model:           "qwen2.5-32b-instruct",
		APIProtocol:     protocol,
		APIKey:          "sk-test",
		MaxOutputTokens: 1_024,
		Timeout:         5 * time.Second,
	}, input)
	return completion, sent, err
}

func streamOf(writer http.ResponseWriter, frames ...string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	for _, frame := range frames {
		_, _ = writer.Write([]byte("data: " + frame + "\n\n"))
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	_, _ = writer.Write([]byte("data: [DONE]\n\n"))
}

// Chat Completions streams a call name and arguments across many frames keyed
// by index. Anything that does not reassemble them sends the model a call with
// no arguments and then reports whatever comes back as the answer.
func TestChatCompletionsStreamReassemblesToolCallsSplitAcrossFrames(t *testing.T) {
	t.Parallel()

	var deltas []string
	completion, sent, err := completeAgainst(t, APIProtocolChatCompletions,
		func(writer http.ResponseWriter, _ *http.Request) {
			streamOf(writer,
				`{"choices":[{"delta":{"content":"先看整体。"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"list_resources","arguments":"{\"api_"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"version\":\"v1\",\"kind\":\"Pod\"}"}}]}}]}`,
				`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":120,"completion_tokens":18,`+
					`"prompt_tokens_details":{"cached_tokens":90}}}`,
			)
		},
		CompletionInput{
			System:   "系统指令",
			Messages: []Message{{Role: RoleUser, Text: "集群怎么样"}},
			Tools: []ToolDefinition{{
				Name: "list_resources", Description: "列出对象",
				Parameters: json.RawMessage(`{"type":"object"}`),
			}},
			OnDelta: func(delta Delta) { deltas = append(deltas, delta.Text) },
		})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if completion.Text != "先看整体。" {
		t.Fatalf("text = %q", completion.Text)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "call_a" || call.Name != "list_resources" ||
		call.Arguments != `{"api_version":"v1","kind":"Pod"}` {
		t.Fatalf("tool call = %+v", call)
	}
	if completion.Usage.InputTokens != 120 || completion.Usage.CachedInputTokens != 90 ||
		completion.Usage.OutputTokens != 18 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
	if !completion.Streamed || completion.FirstToken <= 0 {
		t.Fatalf("streaming was not observed: %+v", completion)
	}
	if len(deltas) != 1 || deltas[0] != "先看整体。" {
		t.Fatalf("deltas = %v", deltas)
	}
	if sent["stream"] != true {
		t.Fatalf("request did not ask for a stream: %+v", sent)
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools were not advertised: %+v", sent["tools"])
	}
}

// A tool result has to reach the endpoint attached to the call that produced
// it, or the model is answering a question it cannot see the answer to.
func TestChatCompletionsCarriesToolResultsBackWithTheirCall(t *testing.T) {
	t.Parallel()

	_, sent, err := completeAgainst(t, APIProtocolChatCompletions,
		func(writer http.ResponseWriter, _ *http.Request) {
			streamOf(writer, `{"choices":[{"delta":{"content":"两个 Pod 不就绪。"}}]}`)
		},
		CompletionInput{Messages: []Message{
			{Role: RoleUser, Text: "集群怎么样"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_a", Name: "cluster_overview", Arguments: "{}"},
			}},
			{Role: RoleTool, ToolCallID: "call_a", ToolName: "cluster_overview", Text: "{}"},
		}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	messages, _ := sent["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %+v", messages)
	}
	assistant, _ := messages[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant message = %+v", assistant)
	}
	result, _ := messages[2].(map[string]any)
	if result["role"] != "tool" || result["tool_call_id"] != "call_a" {
		t.Fatalf("tool message = %+v", result)
	}
}

func TestResponsesStreamAssemblesTextReasoningAndCalls(t *testing.T) {
	t.Parallel()

	completion, sent, err := completeAgainst(t, APIProtocolResponses,
		func(writer http.ResponseWriter, _ *http.Request) {
			streamOf(writer,
				`{"type":"response.reasoning_summary_text.delta","delta":"先看整体"}`,
				`{"type":"response.output_text.delta","delta":"两个 Pod 不就绪。"}`,
				`{"type":"response.output_item.done","item":{"type":"function_call",`+
					`"call_id":"call_b","name":"describe_resource","arguments":"{\"kind\":\"Pod\"}"}}`,
				`{"type":"response.completed","response":{"output":[],`+
					`"usage":{"input_tokens":80,"output_tokens":12,`+
					`"output_tokens_details":{"reasoning_tokens":4}}}}`,
			)
		},
		CompletionInput{
			System:   "系统指令",
			Messages: []Message{{Role: RoleUser, Text: "集群怎么样"}},
			Tools:    []ToolDefinition{{Name: "describe_resource", Parameters: json.RawMessage(`{}`)}},
		})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if completion.Text != "两个 Pod 不就绪。" || completion.Reasoning != "先看整体" {
		t.Fatalf("completion = %+v", completion)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].ID != "call_b" ||
		completion.ToolCalls[0].Name != "describe_resource" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if completion.Usage.InputTokens != 80 || completion.Usage.ReasoningTokens != 4 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
	if sent["instructions"] != "系统指令" {
		t.Fatalf("instructions = %v", sent["instructions"])
	}
}

// Self-hosted endpoints differ on whether they honour `stream`. One that
// answers with a single document is supported by content type rather than by a
// setting an operator has to get right before anything works.
func TestCompleteAcceptsAnEndpointThatIgnoredTheStreamRequest(t *testing.T) {
	t.Parallel()

	completion, _, err := completeAgainst(t, APIProtocolChatCompletions,
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok",` +
				`"tool_calls":[{"id":"call_c","function":{"name":"list_nodes","arguments":"{}"}}]}}],` +
				`"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
		},
		CompletionInput{Messages: []Message{{Role: RoleUser, Text: "节点状态"}}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if completion.Streamed {
		t.Fatal("a single document must not be reported as streamed")
	}
	// There is no first token to measure, and reporting one would put an
	// invented latency into the trail.
	if completion.FirstToken != 0 {
		t.Fatalf("first token = %v", completion.FirstToken)
	}
	if completion.Text != "ok" || len(completion.ToolCalls) != 1 {
		t.Fatalf("completion = %+v", completion)
	}
}

// A step that neither said anything nor called anything cannot be turned into
// progress, and recording it as a successful step would claim work that did not
// happen.
func TestCompleteRefusesAStepWithNothingInIt(t *testing.T) {
	t.Parallel()

	_, _, err := completeAgainst(t, APIProtocolChatCompletions,
		func(writer http.ResponseWriter, _ *http.Request) {
			streamOf(writer, `{"choices":[{"delta":{}}]}`)
		},
		CompletionInput{Messages: []Message{{Role: RoleUser, Text: "集群怎么样"}}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Complete() error = %v, want ErrUnavailable", err)
	}
}

func TestCompleteRefusesANonSuccessStatus(t *testing.T) {
	t.Parallel()

	_, _, err := completeAgainst(t, APIProtocolChatCompletions,
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
		},
		CompletionInput{Messages: []Message{{Role: RoleUser, Text: "集群怎么样"}}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Complete() error = %v, want ErrUnavailable", err)
	}
	// The endpoint own words never travel: that body can quote the request,
	// credential included, and it would end up in the trail and the Console.
	if err != nil && strings.Contains(err.Error(), "slow down") {
		t.Fatalf("endpoint text leaked into the error: %v", err)
	}
}

func TestToolCallAccumulatorNamesAnUnidentifiedCall(t *testing.T) {
	t.Parallel()
	accumulator := newToolCallAccumulator()
	frame := chatToolCall{Index: 0}
	frame.Function.Name = "cluster_overview"
	accumulator.addChat([]chatToolCall{frame})

	calls := accumulator.result()

	// A call the model did not identify still has to be answerable; the
	// position it arrived in is the only stable handle available.
	if len(calls) != 1 || calls[0].ID == "" || calls[0].Arguments != "{}" {
		t.Fatalf("result() = %+v", calls)
	}
}
