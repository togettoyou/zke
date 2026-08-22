package airuntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// toolRunSubtasks delegates independent evidence gathering to bounded branches.
//
// The case for it is narrow and worth stating, because "run things in parallel"
// is the kind of feature that grows until it is a second, unaudited execution
// model. A turn that has to establish resource state, recent Events and metric
// behaviour before it can say anything spends three sequential rounds of model
// latency on three questions that do not depend on each other. Those three are
// what this is for.
//
// What it is not for is splitting a change across executors. A branch cannot
// call a mutating tool at all: the ordering, idempotency and approval
// guarantees of a write are defined on one serial path, and there is no version
// of "two agents applying manifests at once" that keeps them.
const toolRunSubtasks = "run_subtasks"

// maxSubtaskGoalRunes bounds one goal in the arguments the model sends. The
// stamp is bounded again when it is stored; this one exists so an oversized
// goal is refused with an explanation the model can act on rather than being
// silently cut.
const maxSubtaskGoalRunes = 300

// runSubtasksSpec advertises delegation, sized to this deployment's bound.
func runSubtasksSpec(maxParallel int) ToolSpec {
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subtasks": map[string]any{
				"type": "array",
				"description": "互不依赖的取证分支。每个分支是一次独立的只读调查，" +
					"看不到其他分支的过程，只有结论会回到你这里。",
				"minItems": 1,
				"maxItems": maxParallel,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"goal"},
					"properties": map[string]any{
						"goal": map[string]any{
							"type": "string",
							"description": "这个分支要查清的一件事，写成可判定的问题，" +
								"例如“确认 ns/web 下 Deployment api 的副本为什么不就绪”。",
							"maxLength": maxSubtaskGoalRunes,
						},
						"context": map[string]any{
							"type": "string",
							"description": "分支需要而它自己查不到的已知事实，例如用户报告的现象或时间点。" +
								"分支看不到本次对话，只能看到这里写的内容。",
						},
					},
				},
			},
		},
		"required":             []string{"subtasks"},
		"additionalProperties": false,
	})
	if err != nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return ToolSpec{
		Name: toolRunSubtasks,
		Description: fmt.Sprintf(
			"把互不依赖的取证分支交给最多 %d 个并行子任务，各自独立调查后只返回结论、证据和失败原因。"+
				"适合“资源状态 / 近期 Event / 指标异常”这类可以同时进行的分支。"+
				"子任务只有只读工具，不能写入集群，也不能再派生子任务；"+
				"需要按顺序推进、或者下一步依赖上一步结果时，不要用它，直接自己调用工具。",
			maxParallel,
		),
		Schema: schema,
		// Delegation itself reaches no Cluster: every read a branch performs is
		// authorized again, one call at a time, against the same operator and
		// the same session Cluster. `ai.run` is this tool's whole boundary
		// because starting a branch is not itself an access.
		Permissions: []rbac.Permission{rbac.PermissionAIRun},
	}
}

// delegableSpecs is what a branch may call.
//
// Read-only, and without delegation itself. The second exclusion is the depth
// limit from the Phase 4 design expressed as the only thing that can actually
// enforce it: a branch that cannot see the tool cannot recurse, so the bound
// does not depend on a counter anybody could forget to pass down.
func delegableSpecs(specs []ToolSpec) []ToolSpec {
	delegable := make([]ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Mutating || spec.Name == toolRunSubtasks {
			continue
		}
		delegable = append(delegable, spec)
	}
	return delegable
}

// subtaskRun is one branch: what it was asked, and what it came back with.
type subtaskRun struct {
	identity aisession.Subtask
	hint     string
	// text is the branch's own conclusion, and failure the classification when
	// it did not reach one. Exactly one of them is set.
	text     string
	failure  string
	evidence []aisession.Evidence
	steps    int
	calls    int
}

// runSubtasks executes one delegation and folds the branches back into a single
// tool answer.
//
// The parent sees one call and one result, which is what keeps the main line
// readable; the branches' own steps are in the same trail, stamped with the
// branch they belong to, which is what keeps the run reviewable. Nothing
// outlives the call: every branch is waited for here, so a turn that ends —
// normally, cancelled or failed — has no delegated work still running.
func (runtime *Runtime) runSubtasks(
	ctx context.Context, job turnJob, item *plannedCall,
) (ToolResult, error) {
	if runtime.subtask.MaxParallel <= 0 {
		return ToolResult{Text: "本部署没有启用并行子任务。", Failed: true}, nil
	}
	var request struct {
		Subtasks []struct {
			Goal    string `json:"goal"`
			Context string `json:"context"`
		} `json:"subtasks"`
	}
	if err := decodeStrict(item.arguments, &request); err != nil {
		return ToolResult{}, err
	}
	branches := make([]subtaskRun, 0, len(request.Subtasks))
	for index, requested := range request.Subtasks {
		goal := strings.Join(strings.Fields(requested.Goal), " ")
		if goal == "" {
			continue
		}
		if len([]rune(goal)) > maxSubtaskGoalRunes {
			return ToolResult{
				Text: fmt.Sprintf("第 %d 个子任务的 goal 超过 %d 字，请写成更短的、可判定的问题。",
					index+1, maxSubtaskGoalRunes),
				Failed: true,
			}, nil
		}
		branches = append(branches, subtaskRun{
			identity: aisession.Subtask{
				ID:     fmt.Sprintf("%s-%d", item.call.ID, len(branches)+1),
				CallID: item.call.ID,
				Index:  len(branches) + 1,
				Goal:   goal,
			},
			hint: strings.TrimSpace(requested.Context),
		})
	}
	if len(branches) == 0 {
		return ToolResult{
			Text: "没有给出任何子任务目标。请为每个分支写一句要查清的事，或者不要使用这个工具。", Failed: true,
		}, nil
	}
	if len(branches) > runtime.subtask.MaxParallel {
		return ToolResult{
			Text: fmt.Sprintf("一次最多派生 %d 个子任务，这次给了 %d 个。"+
				"请合并成更少的分支，或者分两步进行。", runtime.subtask.MaxParallel, len(branches)),
			Failed: true,
		}, nil
	}
	settings, err := runtime.model.Get(ctx)
	if err != nil {
		return ToolResult{}, err
	}
	specs := delegableSpecs(runtime.ToolCatalogue())
	if len(specs) == 0 {
		return ToolResult{Text: "当前没有任何可供子任务使用的只读工具。", Failed: true}, nil
	}
	var running sync.WaitGroup
	for index := range branches {
		running.Add(1)
		go func(branch *subtaskRun) {
			defer running.Done()
			// One branch's own deadline, inside the turn's. A branch that hangs
			// must not spend the budget its siblings already answered within.
			branchCtx, cancel := context.WithTimeout(ctx, runtime.subtask.Timeout)
			defer cancel()
			runtime.runSubtask(branchCtx, job, branch, settings, specs)
		}(&branches[index])
	}
	running.Wait()
	return foldSubtasks(branches), nil
}

// foldSubtasks is the one answer the main line reads.
//
// Conflicts are not resolved here and must not be: two branches that disagree
// are a fact about the cluster, and hiding it behind a merged summary would
// take the disagreement away from the model whose job it is to reconcile them.
func foldSubtasks(branches []subtaskRun) ToolResult {
	var text strings.Builder
	failed := 0
	evidence := make([]aisession.Evidence, 0, 8)
	seen := make(map[string]struct{})
	for index, branch := range branches {
		if index > 0 {
			text.WriteString("\n\n")
		}
		fmt.Fprintf(&text, "## 子任务 %d：%s\n", branch.identity.Index, branch.identity.Goal)
		fmt.Fprintf(&text, "（%d 个模型步骤、%d 次工具调用）\n", branch.steps, branch.calls)
		if branch.failure != "" {
			failed++
			fmt.Fprintf(&text, "未完成：%s。这一分支没有结论，请不要假设它的答案。", branch.failure)
			continue
		}
		text.WriteString(branch.text)
		for _, item := range branch.evidence {
			key := evidenceKey(item)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			evidence = append(evidence, item)
		}
	}
	// Only a delegation where nothing came back is a failed call. One branch of
	// three failing is a partial answer the main line can still work from, and
	// marking the whole call failed would tell the model to discard the two
	// findings it does have.
	return ToolResult{Text: text.String(), Evidence: evidence, Failed: failed == len(branches)}
}

// runSubtask drives one branch.
//
// It is the same loop as the main turn with three differences, each of which is
// a boundary rather than a simplification: it reads only the entries it wrote
// itself, so a branch cannot see the conversation or a sibling; it never calls
// fail, because a branch that could not finish is a result the main line has to
// reason about and not the end of the turn; and it does not compact, because a
// branch that reached its context window has outgrown what a branch is for and
// should be reported rather than summarized.
func (runtime *Runtime) runSubtask(
	ctx context.Context,
	parent turnJob,
	branch *subtaskRun,
	settings aimodel.Settings,
	specs []ToolSpec,
) {
	job := parent
	identity := branch.identity
	job.subtask = &identity
	definitions := toolDefinitions(specs)
	budget := runtime.budgetFor(settings)
	opening := runtime.append(ctx, job, aisession.AppendInput{
		Kind: aisession.KindSystem,
		Content: aisession.Content{
			Text:  subtaskContextText(job.clusterID, branch.identity, specs),
			Tools: specNames(specs),
		},
		OccurredAt: time.Now().UTC(),
	})
	// The goal belongs to the entry that opens the branch. Repeating it on every
	// row would put a few hundred model-written characters into the trail a
	// dozen times over, for a reader who already knows which branch they are in.
	identity.Goal = ""
	// Everything this branch will write comes after its opening entry, so the
	// per-step rebuild reads the tail of the session rather than all of it.
	// Branches interleave; the stamp is what separates them, not the position.
	after := opening.Sequence - 1
	brief := subtaskBrief(branch.identity, branch.hint)
	repeats := make(map[string]int)
	for step := 1; step <= runtime.subtask.MaxSteps; step++ {
		if ctx.Err() != nil {
			branch.failure = subtaskCancellation(ctx)
			return
		}
		if err := runtime.revalidate(
			ctx, job.userID, job.tenantID, job.projectID, job.clusterID,
		); err != nil {
			branch.failure = aisession.FailurePermissionRevoked
			return
		}
		entries, err := runtime.loadHistoryAfter(ctx, job.sessionID, job.userID, after)
		if err != nil {
			branch.failure = aisession.FailureModelUnavailable
			return
		}
		own := subtaskEntries(entries, branch.identity.ID)
		history, _ := buildMessages(own, branch.identity.ID)
		messages := append(
			[]aimodel.Message{{Role: aimodel.RoleUser, Text: brief}}, history...,
		)
		mode := runtime.currentMode(ctx, job)
		system := subtaskSystemPrompt(job.clusterID, mode, specs)
		pressure := measureMessages(messages, system, definitions)
		if pressure.TotalTokens+budget.maxOutputTokens >= budget.contextWindowTokens {
			branch.failure = aisession.FailureBudgetExceeded
			return
		}
		completion, err := runtime.complete(ctx, job, step, aimodel.CompletionInput{
			System: system, Messages: messages, Tools: definitions,
			MaxOutputTokens: budget.maxOutputTokens,
		})
		if err != nil {
			branch.failure = failureFor(ctx, err)
			return
		}
		branch.steps = step
		requested := make([]string, 0, len(completion.ToolCalls))
		for _, call := range completion.ToolCalls {
			requested = append(requested, call.Name)
		}
		runtime.append(ctx, job, aisession.AppendInput{
			Kind: aisession.KindModel,
			Content: aisession.Content{
				Text: completion.Text, Step: step, Tools: requested,
				Tokens: &aisession.Tokens{
					Input: completion.Usage.InputTokens, CachedInput: completion.Usage.CachedInputTokens,
					Output: completion.Usage.OutputTokens, Reasoning: completion.Usage.ReasoningTokens,
					Context: pressure.TotalTokens, ContextWindow: budget.contextWindowTokens,
				},
				Timing: &aisession.Timing{
					FirstTokenMS: int(completion.FirstToken.Milliseconds()),
					ElapsedMS:    int(completion.Elapsed.Milliseconds()),
					Streamed:     completion.Streamed,
				},
			},
			OccurredAt: time.Now().UTC(), Duration: completion.Elapsed,
		})
		if len(completion.ToolCalls) == 0 {
			branch.text = strings.TrimSpace(completion.Text)
			if branch.text == "" {
				branch.failure = aisession.FailureStepBudget
				return
			}
			branch.evidence = turnEvidence(own, job.turn)
			runtime.append(ctx, job, aisession.AppendInput{
				Kind: aisession.KindConclusion,
				Content: aisession.Content{
					Text: branch.text, Step: step, Evidence: branch.evidence,
				},
				OccurredAt: time.Now().UTC(),
			})
			return
		}
		if branch.calls+len(completion.ToolCalls) > runtime.subtask.MaxToolCalls {
			branch.failure = aisession.FailureToolBudget
			return
		}
		branch.calls += len(completion.ToolCalls)
		if failure := runtime.runToolCalls(
			ctx, job, step, completion.ToolCalls, specs, mode, repeats,
		); failure != "" {
			branch.failure = failure
			return
		}
	}
	branch.failure = aisession.FailureStepBudget
}

// subtaskCancellation separates a branch that hit its own deadline from a turn
// that was cancelled under it. Both stop the branch; only one of them is a
// statement about the branch.
func subtaskCancellation(ctx context.Context) string {
	if ctx.Err() == context.DeadlineExceeded {
		return aisession.FailureModelTimeout
	}
	return aisession.FailureSessionEnded
}

// subtaskEntries is one branch's own trail.
//
// Filtering by the stamp rather than by position is what lets branches
// interleave freely in a single append-only list: a branch reads what it wrote,
// never what a sibling wrote, and never the conversation that spawned it. That
// is the "context snapshot, not a shared mutable message list" rule from the
// Phase 4 design, enforced by the projection rather than by convention.
func subtaskEntries(entries []aisession.Entry, id string) []aisession.Entry {
	own := make([]aisession.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Content.Subtask != nil && entry.Content.Subtask.ID == id {
			own = append(own, entry)
		}
	}
	return own
}
