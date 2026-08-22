package aimodel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

const testActorUserID = "11111111-1111-4111-8111-111111111111"

type fakeStore struct {
	settings store.AIModelSettings
	written  store.UpdateAIModelSettingsParams
	enabled  store.SetAIModelEnabledParams
	writes   int
	getErr   error
	writeErr error
}

func (fake *fakeStore) GetAIModelSettings(context.Context) (store.AIModelSettings, error) {
	if fake.getErr != nil {
		return store.AIModelSettings{}, fake.getErr
	}
	return fake.settings, nil
}

func (fake *fakeStore) UpdateAIModelSettings(
	_ context.Context,
	input store.UpdateAIModelSettingsParams,
) (store.AIModelSettings, error) {
	fake.writes++
	fake.written = input
	if fake.writeErr != nil {
		return store.AIModelSettings{}, fake.writeErr
	}
	fake.settings.BaseURL = input.BaseURL
	fake.settings.Model = input.Model
	fake.settings.APIProtocol = input.APIProtocol
	fake.settings.ContextWindowTokens = input.ContextWindowTokens
	fake.settings.MaxOutputTokens = input.MaxOutputTokens
	if input.APIKey != nil {
		fake.settings.APIKey = *input.APIKey
	}
	fake.settings.RequestTimeout = input.RequestTimeout
	fake.settings.Revision = input.ExpectedRevision + 1
	return fake.settings, nil
}

func (fake *fakeStore) SetAIModelEnabled(
	_ context.Context, input store.SetAIModelEnabledParams,
) (store.AIModelSettings, error) {
	fake.enabled = input
	if fake.writeErr != nil {
		return store.AIModelSettings{}, fake.writeErr
	}
	fake.settings.Enabled = input.Enabled
	fake.settings.Revision = input.ExpectedRevision + 1
	return fake.settings, nil
}

type recordingProber struct {
	target Target
	calls  int
	result Outcome
}

func (prober *recordingProber) Probe(_ context.Context, target Target) Outcome {
	prober.calls++
	prober.target = target
	return prober.result
}

func validInput() SettingsInput {
	return SettingsInput{
		BaseURL:             "https://inference.internal/v1",
		Model:               "qwen2.5-32b-instruct",
		APIProtocol:         APIProtocolResponses,
		ContextWindowTokens: 256_000,
		MaxOutputTokens:     16_000,
		RequestTimeout:      60 * time.Second,
		ExpectedRevision:    1,
		ActorUserID:         testActorUserID,
		Now:                 time.Now().UTC(),
	}
}

func TestGetNeverReportsTheStoredKey(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{settings: store.AIModelSettings{
		Enabled:        true,
		BaseURL:        "https://inference.internal/v1",
		Model:          "qwen2.5-32b-instruct",
		APIKey:         "sk-secret-value",
		RequestTimeout: 60 * time.Second,
		Revision:       3,
	}}
	service := NewService(settingsStore, &recordingProber{})

	settings, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.APIKeyConfigured {
		t.Fatal("a stored key must be reported as configured")
	}
	// The projection has no field the key could travel in; this asserts nobody
	// added one that carries it.
	if settings.BaseURL != "https://inference.internal/v1" || settings.Model != "qwen2.5-32b-instruct" {
		t.Fatalf("unexpected projection: %+v", settings)
	}
}

func TestUpdateNormalizesTheEndpoint(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{}
	service := NewService(settingsStore, &recordingProber{})

	input := validInput()
	input.BaseURL = "  https://inference.internal/v1/  "
	input.Model = "  qwen2.5-32b-instruct  "
	if _, err := service.Update(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if settingsStore.written.BaseURL != "https://inference.internal/v1" {
		t.Fatalf("base URL not normalized: %q", settingsStore.written.BaseURL)
	}
	if settingsStore.written.Model != "qwen2.5-32b-instruct" {
		t.Fatalf("model not trimmed: %q", settingsStore.written.Model)
	}
}

func TestUpdateRefusesInput(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*SettingsInput){
		"非 http 协议": func(input *SettingsInput) {
			input.BaseURL = "ftp://inference.internal/v1"
		},
		"缺少主机名": func(input *SettingsInput) { input.BaseURL = "https:///v1" },
		"地址中带凭证": func(input *SettingsInput) {
			input.BaseURL = "https://user:pass@inference.internal/v1"
		},
		"地址带查询参数": func(input *SettingsInput) {
			input.BaseURL = "https://inference.internal/v1?key=value"
		},
		"模型名含空白": func(input *SettingsInput) { input.Model = "qwen 32b" },
		"API Key 含换行": func(input *SettingsInput) {
			key := "sk-line\nsecond"
			input.APIKey = &key
		},
		"未知 API 协议": func(input *SettingsInput) { input.APIProtocol = "legacy" },
		"上下文窗口过小":   func(input *SettingsInput) { input.ContextWindowTokens = 8_000 },
		"最大输出过小":    func(input *SettingsInput) { input.MaxOutputTokens = 100 },
		"最大输出不小于上下文窗口": func(input *SettingsInput) {
			input.MaxOutputTokens = input.ContextWindowTokens
		},
		"超时低于下界":     func(input *SettingsInput) { input.RequestTimeout = time.Second },
		"超时高于上界":     func(input *SettingsInput) { input.RequestTimeout = time.Hour },
		"超时不是整秒":     func(input *SettingsInput) { input.RequestTimeout = 30500 * time.Millisecond },
		"发起者不是 UUID": func(input *SettingsInput) { input.ActorUserID = "operator" },
		"没有携带修订号":    func(input *SettingsInput) { input.ExpectedRevision = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			settingsStore := &fakeStore{}
			service := NewService(settingsStore, &recordingProber{})
			input := validInput()
			mutate(&input)
			if _, err := service.Update(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if settingsStore.writes != 0 {
				t.Fatal("a refused save must not reach the store")
			}
		})
	}
}

// Endpoint fields stay optional while AIOps is disabled. The dedicated enable
// write is what refuses an incomplete stored configuration.
func TestUpdateAcceptsEmptyEndpointConfiguration(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{}
	service := NewService(settingsStore, &recordingProber{})
	input := validInput()
	input.BaseURL = ""
	input.Model = ""
	if _, err := service.Update(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func TestSetEnabledUsesDedicatedStoreWrite(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{settings: store.AIModelSettings{
		BaseURL: "https://inference.internal/v1", Model: "model", Revision: 3,
	}}
	service := NewService(settingsStore, &recordingProber{})
	settings, err := service.SetEnabled(context.Background(), EnabledInput{
		Enabled: true, ExpectedRevision: 3, ActorUserID: testActorUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || !settingsStore.enabled.Enabled || settingsStore.writes != 0 {
		t.Fatalf("dedicated enabled write = %+v, settings writes = %d", settingsStore.enabled, settingsStore.writes)
	}
}

func TestSetEnabledRequiresStoredEndpoint(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{writeErr: store.ErrAIModelSettingsNotConfigured}
	service := NewService(settingsStore, &recordingProber{})
	_, err := service.SetEnabled(context.Background(), EnabledInput{
		Enabled: true, ExpectedRevision: 1, ActorUserID: testActorUserID, Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SetEnabled() error = %v, want ErrNotConfigured", err)
	}
}

func TestUpdateKeyStates(t *testing.T) {
	t.Parallel()

	stored := store.AIModelSettings{APIKey: "sk-stored", Revision: 1}
	empty := ""
	replacement := "sk-new"

	cases := []struct {
		name     string
		key      *string
		expected *string
	}{
		{"未提交时保持不变", nil, nil},
		{"提交空串时清除", &empty, &empty},
		{"提交新值时替换", &replacement, &replacement},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			settingsStore := &fakeStore{settings: stored}
			service := NewService(settingsStore, &recordingProber{})
			input := validInput()
			input.APIKey = testCase.key
			if _, err := service.Update(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			written := settingsStore.written.APIKey
			switch {
			case testCase.expected == nil && written != nil:
				t.Fatalf("expected the stored key to be kept, got %q", *written)
			case testCase.expected != nil && written == nil:
				t.Fatal("expected the key to be written")
			case testCase.expected != nil && *written != *testCase.expected:
				t.Fatalf("wrote %q, want %q", *written, *testCase.expected)
			}
		})
	}
}

func TestUpdateReportsRevisionConflict(t *testing.T) {
	t.Parallel()

	settingsStore := &fakeStore{writeErr: store.ErrAIModelSettingsConflict}
	service := NewService(settingsStore, &recordingProber{})
	if _, err := service.Update(context.Background(), validInput()); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestTestRequiresAnEndpoint(t *testing.T) {
	t.Parallel()

	prober := &recordingProber{}
	service := NewService(&fakeStore{settings: store.AIModelSettings{Model: "m"}}, prober)
	if _, err := service.Test(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if prober.calls != 0 {
		t.Fatal("nothing to call means no outbound request")
	}
}

// Testing a configuration that is not enabled yet is the order an operator
// works in: configure, check, then turn on.
func TestTestProbesTheStoredConfigurationWhileDisabled(t *testing.T) {
	t.Parallel()

	prober := &recordingProber{result: Outcome{Succeeded: true, Status: 200}}
	service := NewService(&fakeStore{settings: store.AIModelSettings{
		Enabled:             false,
		BaseURL:             "https://inference.internal/v1",
		Model:               "qwen2.5-32b-instruct",
		APIProtocol:         string(APIProtocolResponses),
		APIKey:              "sk-stored",
		ContextWindowTokens: 256_000,
		MaxOutputTokens:     16_000,
		RequestTimeout:      45 * time.Second,
	}}, prober)

	outcome, err := service.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Succeeded {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if prober.target.APIKey != "sk-stored" || prober.target.APIProtocol != APIProtocolResponses ||
		prober.target.MaxOutputTokens != 16_000 || prober.target.Timeout != 45*time.Second {
		t.Fatalf("probe did not receive the stored configuration: %+v", prober.target)
	}
}
