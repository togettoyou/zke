package aimodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	completionMaxResponseBytes = 4 * 1024 * 1024
	// streamMaxLineBytes bounds one SSE line. A misbehaving endpoint that never
	// sends a newline must not be able to grow the Server's memory without
	// bound, and no legitimate frame from either protocol approaches this.
	streamMaxLineBytes = 1024 * 1024
	// errorDetailMaxBytes is how much of a rejected response is read to
	// classify it. Every endpoint puts its reason in the first line or two;
	// anything beyond this is a body that would not have helped.
	errorDetailMaxBytes = 8 * 1024
)

// Role is who a conversation message came from.
//
// The four values are what both supported protocols agree on. A tool result is
// a message rather than a side channel because that is how the model is told
// what its own call returned, and rebuilding it from anything else would make
// the request diverge from the trail that recorded it.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one function call the model asked for.
//
// Arguments stays a string all the way through: it is the model's text, it may
// be invalid JSON, and parsing it early would replace "the model sent this"
// with "the Server thinks the model meant this" in the record.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolDefinition is one tool advertised to the model.
type ToolDefinition struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object. Built by the caller and passed
	// through unchanged so the schema the model sees is the schema the caller
	// validates against.
	Parameters json.RawMessage
}

// Message is one turn of the conversation as the model will see it.
type Message struct {
	Role Role
	Text string
	// ToolCalls belongs to an assistant message that requested tools.
	ToolCalls []ToolCall
	// ToolCallID and ToolName belong to a tool message and tie the result back
	// to the call that produced it.
	ToolCallID string
	ToolName   string
}

type CompletionInput struct {
	// System is the Server own instruction. Separate from Messages so it cannot
	// be displaced by conversation content in either protocol.
	System          string
	Messages        []Message
	Tools           []ToolDefinition
	MaxOutputTokens int
	// OnDelta receives output as it streams. Optional: a nil callback still
	// streams, because streaming is also how first-token latency is measured.
	OnDelta func(Delta)
}

// Delta is one increment of streamed output.
type Delta struct {
	Text      string
	Reasoning string
}

type Usage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	ReasoningTokens   int
}

// Completion is one model call result.
//
// ToolCalls and Text are not exclusive: a model may explain what it is about to
// do and call a tool in the same step, and dropping either half would make the
// trail incomplete.
type Completion struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     Usage
	// FirstToken is how long the endpoint took to produce anything at all, and
	// Elapsed how long the whole call took. Both are measured here rather than
	// derived from entry timestamps, which include the Server own bookkeeping.
	FirstToken time.Duration
	Elapsed    time.Duration
	// Streamed reports whether the endpoint honoured the streaming request. An
	// endpoint that answered with one JSON document is supported, but its
	// FirstToken is not a first-token latency and is left zero.
	Streamed bool
}

// Budget is the exact configuration one model call ran under, returned beside
// the completion so the runtime does not have to guess an endpoint's limits
// from a model name.
type Budget struct {
	ContextWindowTokens int
	MaxOutputTokens     int
}

// Complete runs one model call in the endpoint configured protocol.
//
// The request always asks for a stream. An endpoint that ignores it and answers
// with a single JSON document is handled by content type rather than by
// configuration: self-hosted OpenAI-compatible services differ here, and a
// setting for it would be one more thing an operator has to get right before
// anything works at all.
func (prober *HTTPProber) Complete(
	ctx context.Context,
	target Target,
	input CompletionInput,
) (Completion, error) {
	maximum := input.MaxOutputTokens
	if maximum <= 0 || maximum > target.MaxOutputTokens {
		maximum = target.MaxOutputTokens
	}
	var payload any
	var operationPath string
	switch target.APIProtocol {
	case APIProtocolResponses:
		payload = responsesCompletionRequest(target, input, maximum)
		operationPath = responsesPath
	case APIProtocolChatCompletions:
		payload = chatCompletionRequestFor(target, input, maximum)
		operationPath = chatCompletionsPath
	default:
		return Completion{}, &CallError{Kind: CallInvalidRequest}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, fmt.Errorf("encode model completion: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, target.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost,
		target.BaseURL+operationPath, bytes.NewReader(body))
	if err != nil {
		return Completion{}, fmt.Errorf("build model completion: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	if target.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+target.APIKey)
	}
	started := time.Now()
	response, err := prober.client.Do(request)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return Completion{}, &CallError{Kind: CallTimeout}
		}
		return Completion{}, &CallError{Kind: CallTransport}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, completionMaxResponseBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, &CallError{
			Kind:   classifyStatus(response.StatusCode, errorDetail(response.Body)),
			Status: response.StatusCode,
		}
	}
	var completion Completion
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		completion, err = readCompletionStream(response.Body, target.APIProtocol, input.OnDelta, started)
	} else {
		completion, err = readCompletionDocument(response.Body, target.APIProtocol)
	}
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return Completion{}, &CallError{Kind: CallTimeout}
		}
		return Completion{}, err
	}
	completion.Elapsed = time.Since(started)
	// A step that neither says anything nor calls anything cannot be turned
	// into progress, and reporting it as a successful step would leave the
	// trail claiming work that did not happen.
	if strings.TrimSpace(completion.Text) == "" && len(completion.ToolCalls) == 0 {
		return Completion{}, &CallError{Kind: CallEmptyResponse}
	}
	return completion, nil
}

// errorDetail reads just enough of a rejected response to classify it.
//
// The text is matched against and dropped here; it never reaches a caller, a
// trail entry or a log. An endpoint that answered with something other than the
// shared error envelope still contributes its body, because several
// self-hosted services report an oversized request as plain text.
func errorDetail(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, errorDetailMaxBytes))
	if err != nil || len(payload) == 0 {
		return ""
	}
	var decoded wireError
	if json.Unmarshal(payload, &decoded) == nil {
		parts := make([]string, 0, 3)
		if code, ok := decoded.Error.Code.(string); ok && code != "" {
			parts = append(parts, code)
		}
		if decoded.Error.Type != "" {
			parts = append(parts, decoded.Error.Type)
		}
		if decoded.Error.Message != "" {
			parts = append(parts, decoded.Error.Message)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return string(payload)
}

// --- Chat Completions -------------------------------------------------------

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    int    `json:"index"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type conversationMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatConversationRequest struct {
	Model         string                `json:"model"`
	Messages      []conversationMessage `json:"messages"`
	Tools         []chatTool            `json:"tools,omitempty"`
	MaxTokens     int                   `json:"max_tokens"`
	Stream        bool                  `json:"stream"`
	StreamOptions chatStreamOptions     `json:"stream_options"`
}

func chatCompletionRequestFor(
	target Target, input CompletionInput, maximum int,
) chatConversationRequest {
	messages := make([]conversationMessage, 0, len(input.Messages)+1)
	if strings.TrimSpace(input.System) != "" {
		messages = append(messages, conversationMessage{Role: "system", Content: input.System})
	}
	for _, message := range input.Messages {
		converted := conversationMessage{Role: string(message.Role), Content: message.Text}
		if message.Role == RoleTool {
			converted.ToolCallID = message.ToolCallID
			converted.Name = message.ToolName
		}
		for index, call := range message.ToolCalls {
			encoded := chatToolCall{ID: call.ID, Type: "function", Index: index}
			encoded.Function.Name = call.Name
			encoded.Function.Arguments = call.Arguments
			converted.ToolCalls = append(converted.ToolCalls, encoded)
		}
		messages = append(messages, converted)
	}
	request := chatConversationRequest{
		Model: target.Model, Messages: messages, MaxTokens: maximum, Stream: true,
		StreamOptions: chatStreamOptions{IncludeUsage: true},
	}
	for _, tool := range input.Tools {
		request.Tools = append(request.Tools, chatTool{Type: "function", Function: chatToolFunction{
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
		}})
	}
	return request
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	PromptDetails    struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			Reasoning        string         `json:"reasoning"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		Message struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// --- Responses --------------------------------------------------------------

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responsesConversationRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []responsesTool   `json:"tools,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens"`
	Stream          bool              `json:"stream"`
}

func responsesCompletionRequest(
	target Target, input CompletionInput, maximum int,
) responsesConversationRequest {
	items := make([]json.RawMessage, 0, len(input.Messages))
	appendItem := func(value any) {
		if encoded, err := json.Marshal(value); err == nil {
			items = append(items, encoded)
		}
	}
	for _, message := range input.Messages {
		switch message.Role {
		case RoleTool:
			appendItem(map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Text,
			})
		case RoleAssistant:
			if strings.TrimSpace(message.Text) != "" {
				appendItem(map[string]any{"type": "message", "role": "assistant",
					"content": []map[string]string{{"type": "output_text", "text": message.Text}}})
			}
			for _, call := range message.ToolCalls {
				appendItem(map[string]any{"type": "function_call", "call_id": call.ID,
					"name": call.Name, "arguments": call.Arguments})
			}
		default:
			appendItem(map[string]any{"type": "message", "role": string(message.Role),
				"content": []map[string]string{{"type": "input_text", "text": message.Text}}})
		}
	}
	request := responsesConversationRequest{
		Model: target.Model, Instructions: input.System, Input: items,
		MaxOutputTokens: maximum, Stream: true,
	}
	for _, tool := range input.Tools {
		request.Tools = append(request.Tools, responsesTool{Type: "function",
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return request
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	InputDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responsesTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesOutputItem struct {
	Type      string              `json:"type"`
	CallID    string              `json:"call_id"`
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Arguments string              `json:"arguments"`
	Content   []responsesTextPart `json:"content"`
	Summary   []responsesTextPart `json:"summary"`
}

type responsesDocument struct {
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage        `json:"usage"`
}

type responsesStreamEvent struct {
	Type     string               `json:"type"`
	Delta    string               `json:"delta"`
	Item     *responsesOutputItem `json:"item"`
	Response *responsesDocument   `json:"response"`
}

// --- Decoding ---------------------------------------------------------------

// readCompletionStream consumes an SSE body and assembles one completion.
//
// Both protocols are read by the same loop because the difference between them
// is which frames carry which part, not how the transport works. Deltas are
// forwarded as they arrive; the assembled result is what the caller records.
func readCompletionStream(
	body io.Reader,
	protocol APIProtocol,
	onDelta func(Delta),
	started time.Time,
) (Completion, error) {
	completion := Completion{Streamed: true}
	var text, reasoning strings.Builder
	calls := newToolCallAccumulator()
	scanner := bufio.NewScanner(io.LimitReader(body, completionMaxResponseBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), streamMaxLineBytes)
	emit := func(delta Delta) {
		if delta.Text == "" && delta.Reasoning == "" {
			return
		}
		if completion.FirstToken == 0 {
			completion.FirstToken = time.Since(started)
		}
		text.WriteString(delta.Text)
		reasoning.WriteString(delta.Reasoning)
		if onDelta != nil {
			onDelta(delta)
		}
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		switch protocol {
		case APIProtocolChatCompletions:
			applyChatChunk(data, &completion, calls, emit)
		case APIProtocolResponses:
			applyResponsesEvent(data, &completion, calls, emit, &text, &reasoning)
		}
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, &CallError{Kind: CallTransport}
	}
	completion.Text = strings.TrimSpace(text.String())
	completion.Reasoning = strings.TrimSpace(reasoning.String())
	completion.ToolCalls = calls.result()
	return completion, nil
}

func applyChatChunk(
	data string,
	completion *Completion,
	calls *toolCallAccumulator,
	emit func(Delta),
) {
	var chunk chatStreamChunk
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return
	}
	if chunk.Usage != nil {
		completion.Usage = usageFromChat(*chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		reasoningDelta := choice.Delta.ReasoningContent
		if reasoningDelta == "" {
			reasoningDelta = choice.Delta.Reasoning
		}
		emit(Delta{Text: choice.Delta.Content, Reasoning: reasoningDelta})
		calls.addChat(choice.Delta.ToolCalls)
		// Some endpoints answer a streaming request with one chunk carrying a
		// complete message. Accepting it here costs nothing and is the
		// difference between working and not against those services.
		if choice.Message.Content != "" || len(choice.Message.ToolCalls) > 0 {
			emit(Delta{Text: choice.Message.Content, Reasoning: choice.Message.ReasoningContent})
			calls.addChat(choice.Message.ToolCalls)
		}
	}
}

func applyResponsesEvent(
	data string,
	completion *Completion,
	calls *toolCallAccumulator,
	emit func(Delta),
	text, reasoning *strings.Builder,
) {
	var event responsesStreamEvent
	if json.Unmarshal([]byte(data), &event) != nil {
		return
	}
	switch event.Type {
	case "response.output_text.delta":
		emit(Delta{Text: event.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		emit(Delta{Reasoning: event.Delta})
	case "response.output_item.done":
		if event.Item != nil && event.Item.Type == "function_call" {
			calls.addResponses(*event.Item)
		}
	case "response.completed":
		if event.Response == nil {
			return
		}
		completion.Usage = usageFromResponses(event.Response.Usage)
		// The completed document is authoritative for anything the deltas did
		// not carry: an endpoint that streamed no text still reports its output
		// here, and taking it avoids recording an empty step that was not one.
		assembled, assembledReasoning, assembledCalls := splitResponsesOutput(event.Response.Output)
		if text.Len() == 0 && assembled != "" {
			text.WriteString(assembled)
		}
		if reasoning.Len() == 0 && assembledReasoning != "" {
			reasoning.WriteString(assembledReasoning)
		}
		for _, call := range assembledCalls {
			calls.addResponses(call)
		}
	}
}

// readCompletionDocument handles an endpoint that answered a streaming request
// with one JSON document.
func readCompletionDocument(body io.Reader, protocol APIProtocol) (Completion, error) {
	payload, err := io.ReadAll(io.LimitReader(body, completionMaxResponseBytes+1))
	if err != nil || len(payload) > completionMaxResponseBytes {
		return Completion{}, &CallError{Kind: CallTransport}
	}
	switch protocol {
	case APIProtocolChatCompletions:
		var decoded struct {
			Choices []struct {
				Message struct {
					Content          string         `json:"content"`
					ReasoningContent string         `json:"reasoning_content"`
					ToolCalls        []chatToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage chatUsage `json:"usage"`
		}
		if json.Unmarshal(payload, &decoded) != nil || len(decoded.Choices) == 0 {
			return Completion{}, &CallError{Kind: CallEmptyResponse}
		}
		calls := newToolCallAccumulator()
		calls.addChat(decoded.Choices[0].Message.ToolCalls)
		return Completion{
			Text:      strings.TrimSpace(decoded.Choices[0].Message.Content),
			Reasoning: strings.TrimSpace(decoded.Choices[0].Message.ReasoningContent),
			ToolCalls: calls.result(), Usage: usageFromChat(decoded.Usage),
		}, nil
	case APIProtocolResponses:
		var decoded responsesDocument
		if json.Unmarshal(payload, &decoded) != nil {
			return Completion{}, &CallError{Kind: CallEmptyResponse}
		}
		text, reasoning, items := splitResponsesOutput(decoded.Output)
		calls := newToolCallAccumulator()
		for _, item := range items {
			calls.addResponses(item)
		}
		return Completion{Text: strings.TrimSpace(text), Reasoning: strings.TrimSpace(reasoning),
			ToolCalls: calls.result(), Usage: usageFromResponses(decoded.Usage)}, nil
	default:
		return Completion{}, &CallError{Kind: CallInvalidRequest}
	}
}

func splitResponsesOutput(output []responsesOutputItem) (string, string, []responsesOutputItem) {
	texts := make([]string, 0, len(output))
	summaries := make([]string, 0, len(output))
	calls := make([]responsesOutputItem, 0, len(output))
	for _, item := range output {
		switch item.Type {
		case "function_call":
			calls = append(calls, item)
		case "reasoning":
			for _, part := range item.Summary {
				if part.Text != "" {
					summaries = append(summaries, part.Text)
				}
			}
		default:
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), strings.Join(summaries, "\n"), calls
}

func usageFromChat(usage chatUsage) Usage {
	return Usage{
		InputTokens: usage.PromptTokens, CachedInputTokens: usage.PromptDetails.CachedTokens,
		OutputTokens: usage.CompletionTokens, ReasoningTokens: usage.CompletionDetails.ReasoningTokens,
	}
}

func usageFromResponses(usage responsesUsage) Usage {
	return Usage{
		InputTokens: usage.InputTokens, CachedInputTokens: usage.InputDetails.CachedTokens,
		OutputTokens: usage.OutputTokens, ReasoningTokens: usage.OutputDetails.ReasoningTokens,
	}
}

// toolCallAccumulator reassembles calls that arrive in pieces.
//
// Chat Completions streams a call name and arguments across many frames
// addressed by index; Responses delivers each call whole. Both end up here so
// the rest of the package sees one finished list either way.
type toolCallAccumulator struct {
	order []string
	byKey map[string]*ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byKey: make(map[string]*ToolCall)}
}

func (accumulator *toolCallAccumulator) at(key string) *ToolCall {
	if existing, ok := accumulator.byKey[key]; ok {
		return existing
	}
	created := &ToolCall{}
	accumulator.byKey[key] = created
	accumulator.order = append(accumulator.order, key)
	return created
}

func (accumulator *toolCallAccumulator) addChat(calls []chatToolCall) {
	for _, call := range calls {
		key := fmt.Sprintf("index:%d", call.Index)
		// An endpoint that omits the index but sends distinct ids would collapse
		// every call onto index 0. Keying by id when it disagrees with a call
		// already accumulated at that index keeps them apart.
		if existing, ok := accumulator.byKey[key]; ok &&
			call.ID != "" && existing.ID != "" && existing.ID != call.ID {
			key = "id:" + call.ID
		}
		target := accumulator.at(key)
		if call.ID != "" {
			target.ID = call.ID
		}
		if call.Function.Name != "" {
			target.Name = call.Function.Name
		}
		target.Arguments += call.Function.Arguments
	}
}

func (accumulator *toolCallAccumulator) addResponses(item responsesOutputItem) {
	id := item.CallID
	if id == "" {
		id = item.ID
	}
	target := accumulator.at("id:" + id)
	target.ID = id
	if item.Name != "" {
		target.Name = item.Name
	}
	if item.Arguments != "" {
		target.Arguments = item.Arguments
	}
}

func (accumulator *toolCallAccumulator) result() []ToolCall {
	calls := make([]ToolCall, 0, len(accumulator.order))
	for index, key := range accumulator.order {
		call := accumulator.byKey[key]
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		if call.ID == "" {
			// A call the model did not identify still has to be answerable, and
			// the position it arrived in is the only stable handle available.
			call.ID = fmt.Sprintf("call_%d", index+1)
		}
		if strings.TrimSpace(call.Arguments) == "" {
			call.Arguments = "{}"
		}
		calls = append(calls, *call)
	}
	return calls
}
