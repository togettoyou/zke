package airuntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
)

// Compaction is how one conversation keeps fitting into a context window that
// does not grow.
//
// The shape follows the reference harness: a head-anchored span of the surface
// is replaced by one checkpoint the model writes, while a recent tail stays
// verbatim because that is what the next step actually reasons from. Both
// boundaries come from fractions of the endpoint's own context window rather
// than from absolute token counts, so pointing the same deployment at a wider
// model widens the conversation instead of leaving a stored number wrong.
//
// The span is recorded on the checkpoint entry, not implied by its position:
// the trail is append-only, so a checkpoint is written after the tail it did
// not replace, and "everything before this entry" would silently swallow the
// retained tail on the very next step.

// compactionPlan is one selected reduction before anything has been written.
type compactionPlan struct {
	// shadowed is the head span this plan replaces, in trail order.
	shadowed []aisession.Entry
	// retained is what survives verbatim, priced so the record can say how much
	// of the window the tail is worth keeping.
	retainedTokens int
	// beforeTokens is the surface as the next request would have carried it.
	beforeTokens int
}

// planCompaction chooses what to replace, or reports that nothing can be.
//
// The tail is grown from the end until it is worth at least retainTokens, then
// pushed back to a step boundary so an assistant message keeps the results of
// the calls it made. A plan that would shadow nothing is no plan: a single
// oversized step cannot be repaired by moving the boundary, and saying so is
// better than writing a checkpoint that frees no space.
func planCompaction(entries []aisession.Entry, retainTokens int) (compactionPlan, bool) {
	surface, _ := surfaceOf(entries)
	if len(surface) == 0 {
		return compactionPlan{}, false
	}
	messages, _ := buildMessages(entries, "")
	plan := compactionPlan{beforeTokens: messagesTokens(messages)}
	keepFrom := len(surface)
	for index := len(surface) - 1; index >= 0; index-- {
		if plan.retainedTokens >= retainTokens {
			break
		}
		plan.retainedTokens += entryTokens(surface[index])
		keepFrom = index
	}
	keepFrom = stepBoundary(surface, keepFrom)
	if keepFrom <= 0 {
		return compactionPlan{}, false
	}
	plan.shadowed = surface[:keepFrom]
	plan.retainedTokens = 0
	for _, entry := range surface[keepFrom:] {
		plan.retainedTokens += entryTokens(entry)
	}
	return plan, true
}

// stepBoundary walks a candidate tail start back to the beginning of the step
// it landed inside.
//
// A tail that begins at a tool result would leave that result without the call
// that produced it, which most endpoints reject outright. Walking backwards
// only ever keeps more verbatim, so the retained tail is never smaller than the
// budget asked for.
func stepBoundary(surface []aisession.Entry, keepFrom int) int {
	for keepFrom > 0 {
		switch surface[keepFrom].Kind {
		case aisession.KindToolCall, aisession.KindToolResult,
			aisession.KindApprovalRequest, aisession.KindApprovalDecision,
			aisession.KindReasoning, aisession.KindSystem, aisession.KindError:
			keepFrom--
		default:
			return keepFrom
		}
	}
	return keepFrom
}

// compact reduces one session's model-visible surface and records what it did.
//
// It reports whether the surface actually got smaller. A caller that gets false
// has to stop: retrying a request that already overflowed, against a
// conversation that could not be reduced, only spends the operator's budget on
// the same rejection.
func (runtime *Runtime) compact(
	ctx context.Context,
	job turnJob,
	entries []aisession.Entry,
	budget contextBudget,
	trigger string,
	step int,
	specs []ToolSpec,
) bool {
	plan, planned := planCompaction(entries, runtime.retainFor(entries, budget, trigger))
	if !planned {
		return false
	}
	summary, method := runtime.summarize(ctx, job, plan, specs)
	if strings.TrimSpace(summary) == "" {
		return false
	}
	shadowedEvidence := planEvidence(plan.shadowed)
	after := estimateTokens(checkpointPreamble+"\n\n"+summary) + roleOverheadTokens + plan.retainedTokens
	if after >= plan.beforeTokens {
		// A checkpoint that costs more than what it replaced is not a
		// reduction, and writing it would shrink the conversation's meaning
		// without shrinking the request.
		return false
	}
	runtime.append(ctx, job, aisession.AppendInput{
		Kind: aisession.KindCompaction,
		Content: aisession.Content{
			Text: summary, Step: step, Evidence: shadowedEvidence,
			Compaction: &aisession.Compaction{
				Method:              method,
				Trigger:             trigger,
				BeforeTokens:        plan.beforeTokens,
				AfterTokens:         after,
				ThresholdTokens:     budget.thresholdTokens,
				RetainedTokens:      plan.retainedTokens,
				ContextWindowTokens: budget.contextWindowTokens,
				ShadowedFrom:        plan.shadowed[0].Sequence,
				ShadowedTo:          plan.shadowed[len(plan.shadowed)-1].Sequence,
			},
		},
		OccurredAt: time.Now().UTC(),
	})
	return true
}

// retainFor is how much of the tail this compaction leaves alone.
//
// Ordinary pressure keeps the configured fraction of the window, which is the
// policy the deployment chose. A request the endpoint has already refused is a
// different situation: its own accounting disagrees with ours, and keeping the
// full configured tail can mean there is nothing left to shadow at all. There
// the tail is capped at half of what the surface currently costs, so a
// reduction is always available while the boundary stays balanced.
func (runtime *Runtime) retainFor(
	entries []aisession.Entry, budget contextBudget, trigger string,
) int {
	if trigger != aisession.CompactionTriggerOverflow {
		return budget.retainTokens
	}
	messages, _ := buildMessages(entries, "")
	return min(budget.retainTokens, messagesTokens(messages)/2)
}

// summarize asks the model for a checkpoint, and falls back to a mechanical one.
//
// The request replays the shadowed span behind the same instruction and tool
// schemas the conversation itself uses, and puts the compaction directive last.
// That ordering is not cosmetic: it makes the auxiliary call a prefix of the
// request the endpoint just served, so a provider that caches prefixes reuses
// the cache instead of paying for the whole conversation twice.
//
// A model that will not answer does not get to end the turn. The mechanical
// summary is worse — it is a transcript rather than a brief — but it is
// available exactly when the budget is already tight, which is the situation
// where one more thing that can fail is the last thing anybody needs.
func (runtime *Runtime) summarize(
	ctx context.Context,
	job turnJob,
	plan compactionPlan,
	specs []ToolSpec,
) (string, string) {
	messages, _ := buildMessages(plan.shadowed, "")
	if len(messages) == 0 {
		return "", ""
	}
	request := append(append([]aimodel.Message{}, messages...), aimodel.Message{
		Role: aimodel.RoleUser, Text: compactionInstruction,
	})
	for attempt := 0; attempt <= runtime.compaction.Retries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		completion, _, err := runtime.model.Complete(ctx, aimodel.CompletionInput{
			System:          systemPrompt(job.clusterID, aisession.ApprovalAsk, specs, runtime.skills),
			Messages:        request,
			MaxOutputTokens: runtime.compaction.MaxSummaryTokens,
		})
		if err == nil && strings.TrimSpace(completion.Text) != "" {
			return strings.TrimSpace(completion.Text), aisession.CompactionModelSummary
		}
		if err != nil && !aimodel.IsRetryable(err) {
			break
		}
	}
	return mechanicalSummary(messages), aisession.CompactionSummary
}

// mechanicalSummary renders the shadowed span as a labelled transcript.
//
// Everything it drops is still in the trail and in the export; what it keeps is
// the order the conversation happened in, which is the part a model needs to
// carry on. It is deliberately not clever: this runs when the model-written
// checkpoint could not be produced, and a second thing that can fail here would
// leave the turn with nothing at all.
func mechanicalSummary(messages []aimodel.Message) string {
	var summary strings.Builder
	summary.WriteString("以下是自动压缩后保留的目标、最近工作与证据引用：\n")
	for _, message := range messages {
		text := strings.TrimSpace(message.Text)
		if text == "" && len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				fmt.Fprintf(&summary, "[%s 调用 %s]\n", message.Role, call.Name)
			}
			continue
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&summary, "[%s]\n%s\n", message.Role, text)
	}
	return summary.String()
}

// planEvidence is the distinct evidence the shadowed span produced.
//
// A checkpoint that summarizes away ten reads must not also take away the links
// back to what was read: the references are how a reader checks a conclusion
// that now rests on a summary.
func planEvidence(shadowed []aisession.Entry) []aisession.Evidence {
	seen := make(map[string]struct{})
	result := make([]aisession.Evidence, 0, 8)
	for _, entry := range shadowed {
		for _, item := range entry.Content.Evidence {
			key := evidenceKey(item)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

// contextBudget is the endpoint's window resolved against this deployment's
// compaction policy, recomputed per step rather than stored.
type contextBudget struct {
	contextWindowTokens int
	maxOutputTokens     int
	thresholdTokens     int
	retainTokens        int
}

func (runtime *Runtime) budgetFor(settings aimodel.Settings) contextBudget {
	window := settings.ContextWindowTokens
	budget := contextBudget{
		contextWindowTokens: window,
		maxOutputTokens:     settings.MaxOutputTokens,
		thresholdTokens:     int(float64(window) * runtime.compaction.ThresholdRatio),
		retainTokens:        int(float64(window) * runtime.compaction.RetainRatio),
	}
	// The output budget is reserved out of the window on every request, so a
	// threshold above what is left would let a request through that cannot fit
	// its own answer.
	if usable := window - settings.MaxOutputTokens; budget.thresholdTokens > usable {
		budget.thresholdTokens = usable
	}
	if budget.retainTokens >= budget.thresholdTokens {
		budget.retainTokens = budget.thresholdTokens / 2
	}
	return budget
}

// compactionInstruction is the directive that turns one model call into a
// checkpoint writer.
//
// It is delivered as the last user message rather than as a replacement system
// prompt so the conversation's own instruction and tool schemas stay in front
// of it, which is what makes the call a cache-reusable prefix of the request
// that preceded it. The sections are fixed because the next model to read this
// checkpoint has to find the same things in the same places whether it was
// written today or a week ago.
const compactionInstruction = `现在请你作为这次 Kubernetes 运维对话的上下文压缩器工作。

把上面的对话压缩成一份结构化检查点，让另一个模型能够在没有原始对话的情况下继续这项排查。

严格按下面的 Markdown 结构输出，保留每个小节及其顺序。用简短条目，不要大段叙述。没有内容的小节写“（无）”，不要删掉小节。

## 用户目标
- [用户最初和后续提出的目标，措辞关键处照抄原文]

## 工作区与约束
- [固定的 Cluster、涉及的 Namespace 与对象、审批模式、已知的权限限制]

## 已确认的事实
- [已经通过工具读取核实的结论，写清 Namespace、Kind、名称与关键字段值]

## 已执行的读取
- [调用过的工具及其关键参数，以及它给出的结论；失败的调用写明失败原因]

## 未决问题
- [还没有查证的疑点、被拒绝或失败而需要换路径的事项]

## 当前进展
- [检查点这一刻正在做什么]

## 下一步
- [与最近一次用户请求直接对应的下一个动作，或“（无）”]

规则：
- 使用简体中文。原样保留 Namespace、资源名称、容器名、镜像、错误串、数值和字段路径。
- 忠实保留用户的指令与纠正。
- 不要提到这次压缩请求，也不要说明上下文被压缩过。
- 只输出检查点正文，不要调用任何工具。
- 如果上面的对话里已经有一份检查点，那是更早的一次压缩：保留其中仍然成立的事实，丢弃已经过时的部分，与新信息合并成同一份结构，不要原样抄写。`
