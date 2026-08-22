// Package aimodel owns how ZKE reaches a model: what an operator configured,
// and the single outbound call that checks whether that configuration works.
//
// It is deliberately the only place in the Server that holds an API Key in a
// usable form. Everything above it — the HTTP layer, the Console, later the
// assistant's runtime — works with a projection that reports whether a key is
// configured rather than what it is.
package aimodel

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid AI model setting")
	ErrConflict     = errors.New("AI model settings conflict")
	// ErrNotConfigured reports an operation that needs an endpoint before it
	// can mean anything. Separate from ErrInvalidInput because nothing about
	// the request was wrong: the platform is simply not set up yet.
	ErrNotConfigured = errors.New("AI model endpoint is not configured")
	ErrDisabled      = errors.New("AI model endpoint is disabled")
	ErrUnavailable   = errors.New("AI model endpoint is unavailable")
)

const (
	minRequestTimeout = 5 * time.Second
	// MaxRequestTimeout is exported because it bounds more than this package:
	// an HTTP request that waits for a probe has to outlast the longest model
	// call an operator can configure, or the transport's own deadline would
	// preempt the classification and report the wrong cause.
	MaxRequestTimeout      = 300 * time.Second
	maxBaseURLLength       = 2048
	maxModelLength         = 256
	maxAPIKeyLength        = 4096
	minContextWindowTokens = 16_384
	maxContextWindowTokens = 4_000_000
	minMaxOutputTokens     = 1_024
	maxMaxOutputTokens     = 262_144
)

// APIProtocol is the OpenAI-compatible wire shape used by the endpoint.
// Responses preserves agent state and is preferred for Codex-like runtimes;
// Chat Completions keeps self-hosted inference services usable.
type APIProtocol string

const (
	APIProtocolResponses       APIProtocol = "responses"
	APIProtocolChatCompletions APIProtocol = "chat_completions"
)

func (protocol APIProtocol) valid() bool {
	return protocol == APIProtocolResponses || protocol == APIProtocolChatCompletions
}

type invalidInputError struct {
	detail string
}

func (err invalidInputError) Error() string  { return err.detail }
func (err invalidInputError) Unwrap() error  { return ErrInvalidInput }
func (err invalidInputError) Detail() string { return err.detail }

func invalidInput(detail string) error {
	return invalidInputError{detail: detail}
}

// Settings is what the API returns: the configuration without the credential.
//
// APIKeyConfigured rather than a masked key. A mask is still a fact about the
// value — its length, its prefix — and the only question a configuration form
// has to answer is whether the field needs filling in.
type Settings struct {
	Enabled             bool
	BaseURL             string
	Model               string
	APIProtocol         APIProtocol
	APIKeyConfigured    bool
	ContextWindowTokens int
	MaxOutputTokens     int
	RequestTimeout      time.Duration
	Revision            int64
	UpdatedAt           time.Time
}

// SettingsInput is a whole-section save. The endpoint, the model name and the
// credential are only meaningful together, so a partial update of them would be
// a way to reach a combination the operator never looked at.
//
// APIKey is three-state: nil keeps the stored key, empty clears it, anything
// else replaces it. The stored value is never sent to the browser, so a save
// that did not touch the field has nothing to send back.
type SettingsInput struct {
	BaseURL             string
	Model               string
	APIProtocol         APIProtocol
	APIKey              *string
	ContextWindowTokens int
	MaxOutputTokens     int
	RequestTimeout      time.Duration
	ExpectedRevision    int64
	ActorUserID         string
	Now                 time.Time
}

type EnabledInput struct {
	Enabled          bool
	ExpectedRevision int64
	ActorUserID      string
	Now              time.Time
}

type Store interface {
	GetAIModelSettings(context.Context) (store.AIModelSettings, error)
	UpdateAIModelSettings(context.Context, store.UpdateAIModelSettingsParams) (store.AIModelSettings, error)
	SetAIModelEnabled(context.Context, store.SetAIModelEnabledParams) (store.AIModelSettings, error)
}

// Prober performs one minimal request against an endpoint. An interface so the
// connectivity test can be exercised against every failure an endpoint can
// produce without one of them having to be a real network.
type Prober interface {
	Probe(context.Context, Target) Outcome
}

// Generator is the credential-bearing model call used by the runtime. It is
// kept behind this package so neither the AIOps service nor an HTTP handler can
// ever obtain the stored API key.
type Generator interface {
	Complete(context.Context, Target, CompletionInput) (Completion, error)
}

// Target is one resolved configuration, credential included. It never leaves
// this package other than into the Prober.
type Target struct {
	BaseURL         string
	Model           string
	APIProtocol     APIProtocol
	APIKey          string
	MaxOutputTokens int
	Timeout         time.Duration
}

type Service struct {
	store     Store
	prober    Prober
	generator Generator
}

func NewService(settingsStore Store, prober Prober) *Service {
	service := &Service{store: settingsStore, prober: prober}
	if generator, ok := prober.(Generator); ok {
		service.generator = generator
	}
	return service
}

// Complete calls the enabled endpoint without exposing its credential. The
// returned Budget is the exact configuration used for this call, so the
// runtime does not have to guess limits from a model name.
func (service *Service) Complete(
	ctx context.Context,
	input CompletionInput,
) (Completion, Budget, error) {
	stored, err := service.store.GetAIModelSettings(ctx)
	if err != nil {
		return Completion{}, Budget{}, err
	}
	if !stored.Enabled {
		return Completion{}, Budget{}, ErrDisabled
	}
	if stored.BaseURL == "" || stored.Model == "" || service.generator == nil {
		return Completion{}, Budget{}, ErrNotConfigured
	}
	budget := Budget{
		ContextWindowTokens: int(stored.ContextWindowTokens),
		MaxOutputTokens:     int(stored.MaxOutputTokens),
	}
	completion, err := service.generator.Complete(ctx, Target{
		BaseURL:         stored.BaseURL,
		Model:           stored.Model,
		APIProtocol:     APIProtocol(stored.APIProtocol),
		APIKey:          stored.APIKey,
		MaxOutputTokens: int(stored.MaxOutputTokens),
		Timeout:         stored.RequestTimeout,
	}, input)
	return completion, budget, err
}

func (service *Service) Get(ctx context.Context) (Settings, error) {
	stored, err := service.store.GetAIModelSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return settingsFromStore(stored), nil
}

func (service *Service) Update(ctx context.Context, input SettingsInput) (Settings, error) {
	normalized, err := normalizeSettingsInput(input)
	if err != nil {
		return Settings{}, err
	}
	updated, err := service.store.UpdateAIModelSettings(ctx, store.UpdateAIModelSettingsParams{
		BaseURL:             normalized.BaseURL,
		Model:               normalized.Model,
		APIProtocol:         string(normalized.APIProtocol),
		APIKey:              normalized.APIKey,
		ContextWindowTokens: int32(normalized.ContextWindowTokens),
		MaxOutputTokens:     int32(normalized.MaxOutputTokens),
		RequestTimeout:      normalized.RequestTimeout,
		ExpectedRevision:    normalized.ExpectedRevision,
		ActorUserID:         normalized.ActorUserID,
		Now:                 normalized.Now,
	})
	if errors.Is(err, store.ErrAIModelSettingsConflict) {
		return Settings{}, ErrConflict
	}
	if errors.Is(err, store.ErrAIModelSettingsNotConfigured) {
		return Settings{}, ErrInvalidInput
	}
	if err != nil {
		return Settings{}, err
	}
	return settingsFromStore(updated), nil
}

func (service *Service) SetEnabled(ctx context.Context, input EnabledInput) (Settings, error) {
	if !validation.IsUUID(input.ActorUserID) || input.ExpectedRevision <= 0 || input.Now.IsZero() {
		return Settings{}, ErrInvalidInput
	}
	updated, err := service.store.SetAIModelEnabled(ctx, store.SetAIModelEnabledParams{
		Enabled: input.Enabled, ExpectedRevision: input.ExpectedRevision,
		ActorUserID: input.ActorUserID, Now: input.Now,
	})
	if errors.Is(err, store.ErrAIModelSettingsConflict) {
		return Settings{}, ErrConflict
	}
	if errors.Is(err, store.ErrAIModelSettingsNotConfigured) {
		return Settings{}, ErrNotConfigured
	}
	if err != nil {
		return Settings{}, err
	}
	return settingsFromStore(updated), nil
}

// Test makes one minimal request against the stored configuration and reports
// a classified outcome.
//
// It reads what is stored rather than what a form currently holds: "does this
// work" has to be a question about the configuration that will run, otherwise a
// successful test says nothing about the next run. Saving first is one extra
// click and removes the gap between the two.
//
// Enabled is not required. Configuring an endpoint, checking it, and only then
// turning the assistant on is the order an operator would want; refusing to
// test until enabled would invert it.
func (service *Service) Test(ctx context.Context) (Outcome, error) {
	stored, err := service.store.GetAIModelSettings(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if stored.BaseURL == "" || stored.Model == "" {
		return Outcome{}, ErrNotConfigured
	}
	return service.prober.Probe(ctx, Target{
		BaseURL:         stored.BaseURL,
		Model:           stored.Model,
		APIProtocol:     APIProtocol(stored.APIProtocol),
		APIKey:          stored.APIKey,
		MaxOutputTokens: int(stored.MaxOutputTokens),
		Timeout:         stored.RequestTimeout,
	}), nil
}

func settingsFromStore(stored store.AIModelSettings) Settings {
	return Settings{
		Enabled:             stored.Enabled,
		BaseURL:             stored.BaseURL,
		Model:               stored.Model,
		APIProtocol:         APIProtocol(stored.APIProtocol),
		APIKeyConfigured:    stored.APIKey != "",
		ContextWindowTokens: int(stored.ContextWindowTokens),
		MaxOutputTokens:     int(stored.MaxOutputTokens),
		RequestTimeout:      stored.RequestTimeout,
		Revision:            stored.Revision,
		UpdatedAt:           stored.UpdatedAt,
	}
}

// normalizeSettingsInput trims what an operator typed and refuses what cannot
// be saved, returning the values as they will be stored.
func normalizeSettingsInput(input SettingsInput) (SettingsInput, error) {
	// Identity and revision are not fields on the form. A caller that gets them
	// wrong is not an operator making a typo, so they stay undetailed.
	if !validation.IsUUID(input.ActorUserID) || input.ExpectedRevision <= 0 ||
		input.Now.IsZero() {
		return SettingsInput{}, ErrInvalidInput
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.APIKey != nil {
		trimmed := strings.TrimSpace(*input.APIKey)
		input.APIKey = &trimmed
	}

	baseURL, err := normalizeBaseURL(strings.TrimSpace(input.BaseURL))
	if err != nil {
		return SettingsInput{}, err
	}
	input.BaseURL = baseURL
	if err := validateModel(input.Model); err != nil {
		return SettingsInput{}, err
	}
	if err := validateAPIKey(input.APIKey); err != nil {
		return SettingsInput{}, err
	}
	if !input.APIProtocol.valid() {
		return SettingsInput{}, invalidInput("API 协议必须是 Responses 或 Chat Completions")
	}
	if input.ContextWindowTokens < minContextWindowTokens ||
		input.ContextWindowTokens > maxContextWindowTokens {
		return SettingsInput{}, invalidInput("上下文窗口必须是 16384 至 4000000 tokens")
	}
	if input.MaxOutputTokens < minMaxOutputTokens || input.MaxOutputTokens > maxMaxOutputTokens {
		return SettingsInput{}, invalidInput("单次最大输出必须是 1024 至 262144 tokens")
	}
	// The output budget is reserved out of the context window on every request,
	// so a configuration where it does not fit is one where no request can be
	// built at all. When the conversation is compacted is not configured here:
	// it is a fraction of whichever window this endpoint has, and that fraction
	// is deployment policy in the Server configuration file.
	if input.MaxOutputTokens >= input.ContextWindowTokens {
		return SettingsInput{}, invalidInput("单次最大输出必须小于上下文窗口")
	}
	if input.RequestTimeout < minRequestTimeout || input.RequestTimeout > MaxRequestTimeout ||
		input.RequestTimeout%time.Second != 0 {
		return SettingsInput{}, invalidInput("请求超时必须是 5 至 300 秒的整秒数")
	}
	return input, nil
}

// normalizeBaseURL accepts an empty value — that is the unconfigured state —
// and otherwise requires an absolute http(s) URL naming a host.
//
// Query and fragment are refused rather than dropped: the Server appends an
// operation path to this value, and a stored query string would end up in the
// middle of the request target, producing a URL the operator never wrote.
// Credentials in the URL are refused because they would be a second place a
// secret lives, outside the one column that is handled as one.
func normalizeBaseURL(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > maxBaseURLLength {
		return "", invalidInput("接入地址过长")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", invalidInput("接入地址不是合法的 URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", invalidInput("接入地址必须以 http:// 或 https:// 开头")
	}
	if parsed.Host == "" {
		return "", invalidInput("接入地址必须包含主机名")
	}
	if parsed.User != nil {
		return "", invalidInput("接入地址中不能携带用户名或密码，API Key 请填在下面的字段")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", invalidInput("接入地址不能包含查询参数或锚点")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func validateModel(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxModelLength {
		return invalidInput("模型名过长")
	}
	if containsSpaceOrControl(value) {
		return invalidInput("模型名不能包含空白或控制字符")
	}
	return nil
}

// validateAPIKey refuses anything that could not be sent as a header value.
// A pasted key carrying a newline would otherwise fail at request time with an
// error about header construction, which says nothing to whoever pasted it.
func validateAPIKey(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	if len(*value) > maxAPIKeyLength {
		return invalidInput("API Key 过长")
	}
	if containsSpaceOrControl(*value) {
		return invalidInput("API Key 不能包含空白或控制字符")
	}
	return nil
}

func containsSpaceOrControl(value string) bool {
	return strings.ContainsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	})
}
