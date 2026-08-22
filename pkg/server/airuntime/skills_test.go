package airuntime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
)

type stubSkills struct{ skills []Skill }

func (library stubSkills) Skills() []Skill { return library.skills }

func skillRuntime(sessions *memorySessions, model ModelService, skills ...Skill) *Runtime {
	return New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{
		Tools:  readOnlyTools(),
		Skills: stubSkills{skills: skills},
	})
}

func loading(id string) aimodel.Completion {
	arguments, err := json.Marshal(map[string]string{"skill": id})
	if err != nil {
		panic(err)
	}
	return calling(toolLoadSkill, string(arguments))
}

// A playbook is Server-shipped text and the one tool answer in the system that
// is not derived from a Cluster. It is marked as such where it enters the
// conversation, because the instruction that cluster content is data travels
// with the content — and a body labelled untrusted is a body the model has been
// told not to follow.
func TestLoadedSkillIsRecordedAsTrusted(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		loading("crashloop"),
		answering("按流程排查完毕。"),
	}}
	runtime := skillRuntime(sessions, model, Skill{
		ID: "crashloop", Title: "Pod 反复重启", Summary: "重启排查顺序。",
		Tools: []string{"list_resources"}, Body: "1. 先看 Event。2. 再读上一实例日志。",
	})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "Pod 一直重启", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolLoadSkill)
	if result == nil {
		t.Fatalf("skill was not loaded: %v", entryKinds(sessions))
	}
	if result.Content.Untrusted {
		t.Fatal("a Server-shipped playbook must not be recorded as cluster data")
	}
	if !strings.Contains(result.Content.Text, "再读上一实例日志") {
		t.Fatalf("playbook body missing:\n%s", result.Content.Text)
	}
	if !strings.Contains(toolResultText(*result), "ZKE 平台内容") {
		t.Fatalf("model was not told the body is trusted:\n%s", toolResultText(*result))
	}
}

// A playbook whose steps this deployment cannot take is worse than no playbook:
// the model plans around the step and only then discovers the tool is absent.
func TestSkillNeedingAnAbsentToolIsNotOffered(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	runtime := skillRuntime(sessions, &scriptedModel{},
		Skill{ID: "usable", Summary: "可用。", Tools: []string{"list_resources"}, Body: "步骤。"},
		Skill{ID: "metrics", Summary: "需要指标。", Tools: []string{"query_metrics"}, Body: "步骤。"},
	)

	offered := skillIDs(runtime.SkillCatalogue())
	if !slices.Contains(offered, "usable") || slices.Contains(offered, "metrics") {
		t.Fatalf("offered skills = %v, want only the one this deployment can carry out", offered)
	}
	if !strings.Contains(
		systemPrompt(testClusterID, aisession.ApprovalAsk,
			runtime.ToolCatalogue(), runtime.SkillCatalogue()),
		"usable：可用。",
	) {
		t.Fatal("the usable skill is missing from the system prompt index")
	}
}

// The tool disappears with the library rather than staying and failing. An
// advertised tool costs a slot in every request and a step every time a model
// believes in it.
func TestSkillToolIsAbsentWithoutALibrary(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{},
		activeUsers{true}, Config{Tools: readOnlyTools()})
	if slices.Contains(specNames(runtime.ToolCatalogue()), toolLoadSkill) {
		t.Fatal("skill tool advertised with no library composed")
	}
}

// An id the library does not have is answered with what it does have, so the
// model corrects itself in the next step instead of spending one on a guess.
func TestUnknownSkillNamesTheAvailableOnes(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		loading("does-not-exist"),
		answering("改用自己的顺序。"),
	}}
	runtime := skillRuntime(sessions, model, Skill{
		ID: "crashloop", Summary: "重启排查。", Tools: []string{"list_resources"}, Body: "步骤。",
	})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolLoadSkill)
	if result == nil || !result.Content.Failed {
		t.Fatalf("unknown skill was not refused: %+v", result)
	}
	if !strings.Contains(result.Content.Text, "crashloop") {
		t.Fatalf("refusal does not say what is available:\n%s", result.Content.Text)
	}
}

// A skill carries no evidence: a playbook is not something that happened in a
// Cluster, and a conclusion citing one would put a row in the evidence list
// that leads nowhere an operator can check.
func TestSkillProducesNoEvidence(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		loading("crashloop"),
		answering("完成。"),
	}}
	runtime := skillRuntime(sessions, model, Skill{
		ID: "crashloop", Summary: "重启排查。", Tools: []string{"list_resources"}, Body: "步骤。",
	})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolLoadSkill)
	if result == nil {
		t.Fatalf("skill was not loaded: %v", entryKinds(sessions))
	}
	if len(result.Content.Evidence) != 0 {
		t.Fatalf("a playbook produced evidence: %+v", result.Content.Evidence)
	}
}
