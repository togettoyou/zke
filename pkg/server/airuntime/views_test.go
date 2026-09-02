package airuntime

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

func opening(arguments map[string]any) aimodel.Completion {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		panic(err)
	}
	return calling(toolOpenConsoleView, string(encoded))
}

func openingChart(reason string) aimodel.Completion {
	return opening(map[string]any{
		"show":       "metric",
		"reason":     reason,
		"expression": `sum(container_memory_working_set_bytes)`,
	})
}

func viewRuntime(sessions *memorySessions, model ModelService, authorizer Authorizer) *Runtime {
	return New(context.Background(), sessions, model, authorizer, activeUsers{true}, Config{
		Tools: readOnlyTools(),
	})
}

// The intent is durable, and it is the tool result that carries it. A desktop
// that moved is part of the record: the export, a later review and the operator
// reopening the conversation next week all read the same entry.
func TestOpenedViewIsRecordedOnTheToolResult(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		openingChart("内存曲线本身就是答案，直接给你打开。"),
		answering("最近一小时内存稳定在 3.2 GiB。"),
	}}
	runtime := viewRuntime(sessions, model, allowAuthorizer{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID,
		Text: "看看最近一小时的内存", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolOpenConsoleView)
	if result == nil {
		t.Fatalf("no view was opened: %v", entryKinds(sessions))
	}
	view := result.Content.View
	if view == nil {
		t.Fatalf("tool result carries no view intent: %+v", result.Content)
	}
	if view.Target.Cluster != testClusterID {
		t.Fatalf("view cluster = %q, want the session workspace %q", view.Target.Cluster, testClusterID)
	}
	// A chart the operator still has to press a button to see is the last step
	// of the job left undone, which is the whole reason the tool exists.
	if !view.Run {
		t.Fatal("a metric view opened without being told to run its query")
	}
	if view.Reason == "" {
		t.Fatal("an action the operator did not take was recorded without a reason")
	}
	// The answer is the Server's own sentence about a Console, not something a
	// Cluster returned.
	if result.Content.Untrusted {
		t.Fatal("the confirmation was recorded as cluster data")
	}
}

// One turn may take the screen once. Beyond that an answer has stopped being an
// answer and become remote control of somebody else's desktop — and the Console
// applies the same bound, so a model told otherwise would be describing a
// desktop that only moved once.
func TestSecondViewInOneTurnIsRefused(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		openingChart("先看内存。"),
		openingChart("再看一眼别的。"),
		answering("两张图都在桌面上。"),
	}}
	runtime := viewRuntime(sessions, model, allowAuthorizer{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "都给我打开", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	opened := 0
	refused := false
	sessions.mu.Lock()
	for _, entry := range sessions.entries {
		if entry.Kind != aisession.KindToolResult || entry.Content.Tool != toolOpenConsoleView {
			continue
		}
		if entry.Content.View != nil {
			opened++
		} else if entry.Content.Failed {
			refused = true
		}
	}
	sessions.mu.Unlock()
	if opened != 1 || !refused {
		t.Fatalf("opened %d views, refusal recorded = %t; want exactly one open and one refusal",
			opened, refused)
	}
}

// Opening a window is navigation, not access — but a window onto a view the
// Server would refuse is a dead end dressed up as an answer, so the target is
// authorized with the permission its own kind answers to.
func TestViewIsRefusedWithoutThePermissionItsTargetNeeds(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		openingChart("给你打开监控。"),
		answering("你没有指标权限，我改用对象状态说明。"),
	}}
	runtime := viewRuntime(sessions, model, evidenceAuthorizer{
		denied: rbac.PermissionClusterMetricsRead,
	})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看看内存", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolOpenConsoleView)
	if result == nil {
		t.Fatalf("the call was not recorded: %v", entryKinds(sessions))
	}
	if result.Content.View != nil {
		t.Fatalf("a view was opened without the permission to read it: %+v", result.Content.View)
	}
	if !result.Content.Failed {
		t.Fatal("the refusal was not reported to the model")
	}
}

// Permissions are rechecked per call, and reading the trail is one more moment
// where the answer may have changed. An entry still offering to open a window
// the Server now refuses would be an offer with nothing behind it.
func TestTrajectoryDropsAViewTheOperatorMayNoLongerOpen(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk), entries: []aisession.Entry{{
		Sequence: 1, Turn: 1, Kind: aisession.KindToolResult,
		Content: aisession.Content{
			Tool: toolOpenConsoleView,
			View: &aisession.View{
				Target: aisession.Evidence{
					Kind: aisession.EvidenceMetric, Cluster: testClusterID, Query: "node_cpu",
				},
				Run: true, Reason: "打开 CPU 曲线。",
			},
		},
	}}}
	runtime := New(context.Background(), sessions, &scriptedModel{}, evidenceAuthorizer{
		denied: rbac.PermissionClusterMetricsRead,
	}, activeUsers{true}, Config{})

	entries, err := runtime.Trajectory(context.Background(), aisession.TrajectoryQuery{
		SessionID: testSessionID, InitiatorUserID: testUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Content.View != nil {
		t.Fatalf("a revoked view survived the read: %+v", entries[0].Content.View)
	}
}

// The navigation hints are resolved on the way out, from Cluster identity,
// exactly as they are for evidence: they are how the Console knows which
// Project to switch to, and they are never taken from the model.
func TestTrajectoryResolvesViewNavigationScope(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk), entries: []aisession.Entry{{
		Sequence: 1, Turn: 1, Kind: aisession.KindToolResult,
		Content: aisession.Content{
			Tool: toolOpenConsoleView,
			View: &aisession.View{
				Target: aisession.Evidence{
					Kind: aisession.EvidenceResource, Cluster: testClusterID,
					GVK: "apps/v1/Deployment", Name: "web",
				},
				Reason: "直接打开这个工作负载。",
			},
		},
	}}}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{},
		activeUsers{true}, Config{})

	entries, err := runtime.Trajectory(context.Background(), aisession.TrajectoryQuery{
		SessionID: testSessionID, InitiatorUserID: testUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	target := entries[0].Content.View.Target
	if target.TenantID == "" || target.ProjectID != testProjectID {
		t.Fatalf("view navigation scope = %+v", target)
	}
}

// A branch answers to another model, and it has no screen to move. The turn's
// one desktop move belongs to the main line, which is the part actually talking
// to the person.
func TestSubtasksCannotOpenViews(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	runtime := subtaskRuntime(sessions, &scriptedModel{}, readOnlyTools(), 2)
	if slices.Contains(specNames(delegableSpecs(runtime.ToolCatalogue())), toolOpenConsoleView) {
		t.Fatal("a delegated branch was offered the tool that moves the operator's desktop")
	}
}

// A window that opens on nothing is worse than one that does not open: the
// operator was interrupted to be shown an empty chart. The check belongs to the
// tool so the model gets a correction rather than a silent no-op.
func TestViewTargetRefusesTargetsNoScreenCanShow(t *testing.T) {
	t.Parallel()
	cases := map[string]openConsoleViewRequest{
		"metric without a question":  {Show: "metric"},
		"metric with two questions":  {Show: "metric", Query: "node_cpu", Expression: "up"},
		"resource without a Kind":    {Show: "resource", Name: "web"},
		"log without a Pod":          {Show: "log", Namespace: "team-a"},
		"release without a name":     {Show: "helm_release", Namespace: "team-a"},
		"a view that does not exist": {Show: "terminal"},
	}
	for name, request := range cases {
		if _, err := viewTarget(request); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	target, err := viewTarget(openConsoleViewRequest{
		Show: "resource", APIVersion: "apps/v1", Kind: "Deployment", Name: "web",
	})
	if err != nil || target.GVK != "apps/v1/Deployment" {
		t.Fatalf("viewTarget() = %+v, %v", target, err)
	}
}

func TestNamedMetricViewMustExistInTheComposedCatalogue(t *testing.T) {
	t.Parallel()

	for name, queries := range map[string]map[string]bool{
		"known":   {"gpu_utilization": true},
		"unknown": {},
	} {
		t.Run(name, func(t *testing.T) {
			sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
			tools := readOnlyTools()
			tools.metricQueries = queries
			model := &scriptedModel{steps: []aimodel.Completion{
				opening(map[string]any{
					"show": "metric", "reason": "打开 GPU 曲线。", "query": "gpu_utilization",
				}),
				answering("完成。"),
			}}
			runtime := New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{Tools: tools})
			if _, err := runtime.Start(context.Background(), StartInput{
				SessionID: testSessionID, UserID: testUserID, Text: "打开 GPU", Now: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			runtime.Wait()
			result := mainLineResult(sessions, toolOpenConsoleView)
			if result == nil {
				t.Fatalf("no tool result recorded: %v", entryKinds(sessions))
			}
			if name == "known" && result.Content.View == nil {
				t.Fatal("known metric query was refused")
			}
			if name == "unknown" && !result.Content.Failed {
				t.Fatal("unknown metric query was not refused")
			}
		})
	}
}
