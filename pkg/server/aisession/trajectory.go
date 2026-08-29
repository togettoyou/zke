// Package aisession holds an AIOps session and the trail of what it did.
//
// A session is a long-lived object: a title, a series of turns, something an
// operator comes back to days later. A turn is one question through to its
// answer. An entry is one step of the trail.
//
// Every step is written here before it is pushed to whoever is watching. That
// order is the whole point — it is what makes closing the window harmless, what
// lets a reconnect resume exactly where it left off, and what makes a review
// afterwards see the same thing the operator saw at the time.
//
// The package deliberately knows nothing about models, tools or Skills. It
// records what happened; deciding what happens next is the runtime's job.
package aisession

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is what an entry is. Fixed vocabulary, mirrored by the database, because
// an entry nobody can render is an entry nobody should be able to store.
type Kind string

const (
	// KindSystem is the instruction part: the runtime's rules, and the
	// permissions and tools this turn was actually granted. AIOps' claim about
	// its own boundary is this entry, which is what makes it checkable rather
	// than decorative.
	KindSystem Kind = "system"
	// KindInput is the question that opened the turn, with whatever the
	// operator had selected when they asked.
	KindInput Kind = "input"
	// KindContext is cluster content on its way into the model, always marked
	// untrusted. See §10 of the Phase 4 design: anything anybody can write into
	// a cluster is data, never instruction.
	KindContext Kind = "context"
	// KindModel is one step of model output: what the model said, what it
	// decided to call, and what the call cost.
	KindModel Kind = "model"
	// KindReasoning is a model reasoning summary, kept apart from KindModel so
	// the Console can fold it away without hiding the answer, and so a reader
	// can tell an explanation of intent from a statement of fact.
	KindReasoning Kind = "reasoning"
	// KindToolCall is one tool invocation with its arguments, its target
	// cluster and the authorization decision that let it through.
	KindToolCall Kind = "tool_call"
	// KindToolResult is what that call returned, with the evidence it produced.
	KindToolResult Kind = "tool_result"
	// KindApprovalRequest is a call the session approval mode will not run
	// without a person. The turn is parked on it until KindApprovalDecision
	// answers, and both are in the trail because who allowed a sensitive read
	// is part of what happened.
	KindApprovalRequest Kind = "approval_request"
	// KindApprovalDecision is the answer to one request.
	KindApprovalDecision Kind = "approval_decision"
	// KindCompaction records an automatic context compression. It is visible in
	// the trail because changing the retained context can change later answers.
	KindCompaction Kind = "compaction"
	// KindConclusion is one conclusion and the evidence it rests on.
	KindConclusion Kind = "conclusion"
	// KindError is a classified failure.
	KindError Kind = "error"
)

var kinds = []Kind{
	KindSystem, KindInput, KindContext, KindModel, KindReasoning,
	KindToolCall, KindToolResult, KindApprovalRequest, KindApprovalDecision,
	KindCompaction, KindConclusion, KindError,
}

func (kind Kind) valid() bool {
	return slices.Contains(kinds, kind)
}

// Status is where a session is. Sessions do not fail; turns do.
type Status string

const (
	// StatusIdle is a session waiting for its next question.
	StatusIdle Status = "idle"
	// StatusWorking is a session with a turn being driven right now.
	StatusWorking Status = "working"
)

// ApprovalMode is how far a session may go without asking. The operator picks
// it in the composer and may switch whenever they like, including while a turn
// is running.
//
// None of them grants anything: the ceiling is always that operator's own
// permissions, and the modes differ only in who presses the button. What they
// do change is how far a prompt injection out of a Pod log can reach, which is
// why the mode a turn ran under is part of its record.
type ApprovalMode string

const (
	// ApprovalAsk stops for a person before every write and before a Secret's
	// values are read.
	ApprovalAsk ApprovalMode = "ask"
	// ApprovalAssisted runs ordinary changes on its own and stops only at what
	// ZKE already calls a sensitive operation — deletion, Secrets, RBAC,
	// draining a Node, the protected Namespaces.
	//
	// The middle mode borrows that definition rather than inventing a second
	// notion of "risky": the operations ZKE already makes a person confirm are
	// exactly the ones worth stopping at here.
	ApprovalAssisted ApprovalMode = "assisted"
	// ApprovalFull does everything the operator's permissions allow without
	// stopping.
	ApprovalFull ApprovalMode = "full"
)

var approvalModes = []ApprovalMode{ApprovalAsk, ApprovalAssisted, ApprovalFull}

// Valid reports whether a mode is one this package knows. Exported because the
// runtime re-reads the mode mid-turn and has to decide what an unrecognized
// stored value means without guessing on the permissive side.
func (mode ApprovalMode) Valid() bool {
	return slices.Contains(approvalModes, mode)
}

// TurnStatus is how one turn ended.
type TurnStatus string

const (
	TurnSucceeded TurnStatus = "succeeded"
	TurnFailed    TurnStatus = "failed"
	TurnCanceled  TurnStatus = "canceled"
)

// The failures a turn can end on, as a fixed vocabulary rather than free text.
//
// A classification is something an operator can act on, a Console can
// translate and a query can filter by; a sentence from a model or a cluster is
// none of those, and would put text from outside ZKE into a record that is
// supposed to be ZKE's account of what happened. Every value here is a failure
// path the Phase 4 design names.
const (
	FailureModelUnavailable = "model_unavailable"
	FailureModelTimeout     = "model_timeout"
	// FailureModelRateLimited is an endpoint that kept asking for less traffic
	// for longer than the retry policy was willing to wait.
	FailureModelRateLimited = "model_rate_limited"
	// FailureModelQuota is an exhausted account balance. Waiting does not fix
	// it, which is why it is not reported as a passing unavailability.
	FailureModelQuota = "model_quota_exhausted"
	// FailureModelRejected is a request or a credential the endpoint refused
	// outright: a configuration to correct rather than a call to repeat.
	FailureModelRejected     = "model_rejected"
	FailurePermissionRevoked = "permission_revoked"
	FailureSessionEnded      = "session_ended"
	FailureBudgetExceeded    = "budget_exceeded"
	FailureAgentOffline      = "agent_offline"
	FailureContextCompaction = "context_compaction_failed"
	// FailureStepBudget is a turn that used every model step it was allowed
	// without reaching a conclusion. Ending it is honest; letting the loop run
	// on would spend an operator budget on a model that is not converging.
	FailureStepBudget = "step_budget_exhausted"
	// FailureToolBudget is the same limit applied to tool calls.
	FailureToolBudget = "tool_budget_exhausted"
	// FailureApprovalTimeout is a turn parked on an approval nobody answered.
	FailureApprovalTimeout = "approval_timeout"
	// FailureInterrupted is a turn whose Server process is gone. Nothing is
	// driving it any more, so it is ended at startup rather than left showing
	// as a turn that never advances.
	FailureInterrupted = "interrupted"
)

var failures = []string{
	FailureModelUnavailable,
	FailureModelTimeout,
	FailureModelRateLimited,
	FailureModelQuota,
	FailureModelRejected,
	FailurePermissionRevoked,
	FailureSessionEnded,
	FailureBudgetExceeded,
	FailureAgentOffline,
	FailureContextCompaction,
	FailureStepBudget,
	FailureToolBudget,
	FailureApprovalTimeout,
	FailureInterrupted,
}

// How an approval request was answered. A fixed pair rather than free text: it
// is a decision the Console renders and an auditor filters by.
const (
	DecisionApproved = "approved"
	DecisionDenied   = "denied"
)

func validDecision(decision string) bool {
	return decision == DecisionApproved || decision == DecisionDenied
}

func validFailure(failure string) bool {
	return slices.Contains(failures, failure)
}

// Session is one session, without its entries.
type Session struct {
	ID              string
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	Title           string
	Status          Status
	ApprovalMode    ApprovalMode
	// CurrentTurn is 0 in a session nobody has asked anything in yet.
	CurrentTurn     int32
	LastTurnStatus  TurnStatus
	LastTurnFailure string
	CreatedAt       time.Time
	LastActivityAt  time.Time
	ArchivedAt      *time.Time
}

// Entry is one step of a trail.
type Entry struct {
	Sequence   int32
	Turn       int32
	Kind       Kind
	OccurredAt time.Time
	Duration   time.Duration
	// Truncated reports that the Server cut this body down. Rendering an
	// excerpt as if it were whole is how a trail starts lying quietly.
	Truncated bool
	Content   Content
}

// Content is an entry's body. Which fields carry anything depends on the kind,
// and one shape rather than eight is deliberate: they are read together, in
// order, always all of them, and the only thing done with the difference is
// rendering it.
type Content struct {
	Text string `json:"text,omitempty"`
	// Step is which model step inside the turn produced this entry, counting
	// from 1. A turn is a loop now, not a single call: without this the trail
	// says what happened but not which round of thinking it belongs to.
	Step int `json:"step,omitempty"`
	// Tool names the tool a call or a result belongs to.
	Tool string `json:"tool,omitempty"`
	// CallID ties a call, its approval and its result together. It is the
	// identifier the model itself used, so the record matches what was sent
	// back to it.
	CallID string `json:"call_id,omitempty"`
	// Arguments is the call's arguments as JSON text, carried as a string so
	// that truncating it cannot produce a document that claims to be parseable
	// and is not.
	Arguments string `json:"arguments,omitempty"`
	// Target is the cluster, Namespace and object a call addressed. A session
	// may span clusters, so an entry that did not say where it went would make
	// the trail unreadable — and unauditable.
	Target *Target `json:"target,omitempty"`
	// Authorized records the decision that let a call through. Each call is a
	// full authorization check of its own, and this is where that shows.
	Authorized *bool `json:"authorized,omitempty"`
	// Failed marks a tool result that did not produce what it was asked for.
	// The model is told the same thing, so a failure it then reasons around is
	// visible as a failure here rather than as an ordinary result.
	Failed bool `json:"failed,omitempty"`
	// Evidence is what a result produced or a conclusion rests on.
	Evidence   []Evidence  `json:"evidence,omitempty"`
	Tokens     *Tokens     `json:"tokens,omitempty"`
	Timing     *Timing     `json:"timing,omitempty"`
	Compaction *Compaction `json:"compaction,omitempty"`
	// Decision answers an approval request: approved or denied.
	Decision string `json:"decision,omitempty"`
	// Tools lists what the turn was allowed to call. Carried on the system
	// entry that opens a turn: what AIOps could have done is as much part of
	// the record as what it did.
	Tools []string `json:"tools,omitempty"`
	// Failure classifies an error entry.
	Failure string `json:"failure,omitempty"`
	// Mode is the approval mode in force. Carried on the `system` entry that
	// opens a turn, and on the one written when the operator switches mid-turn,
	// so the trail says which mode each part of a turn ran under.
	Mode ApprovalMode `json:"mode,omitempty"`
	// Untrusted marks content that came out of a cluster. Set by this package
	// on every context entry rather than left to the caller: it is an invariant,
	// not an option.
	Untrusted bool `json:"untrusted,omitempty"`
	// Subtask names the delegated branch an entry belongs to. Absent on the
	// main line of a turn, which is what makes "the turn itself" and "one of
	// its branches" distinguishable in a single append-only list.
	Subtask *Subtask `json:"subtask,omitempty"`
}

// Subtask identifies one delegated investigation branch inside a turn.
//
// Branches write into the same trail as the turn that spawned them, because a
// second store would be a second account of the same run and the two could
// disagree. What keeps them readable is this stamp: every entry says which
// branch produced it, so the Console can fold a branch away and a review can
// tell a fact the main line established from one a branch reported.
type Subtask struct {
	// ID is the branch identity, unique inside one session.
	ID string `json:"id"`
	// CallID is the parent tool call that delegated this branch. It is what
	// ties a branch back to the one call the main line sees.
	CallID string `json:"call_id"`
	// Index is the branch's 1-based position in that call, so labels and order
	// stay stable however the branches interleave.
	Index int `json:"index"`
	// Goal is what the branch was asked to find out. Carried on the entry that
	// opens the branch; the rest of its entries only need the identity.
	Goal string `json:"goal,omitempty"`
}

type Target struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace,omitempty"`
	GVK       string `json:"gvk,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Tokens is what one model step cost and how full the context was when it ran.
//
// Context is the request's total pressure as the runtime measured it, and
// ContextWindow the endpoint's window at that moment. Both are recorded rather
// than derived later because the endpoint's configuration can change between a
// turn happening and somebody reading it back.
type Tokens struct {
	Input         int `json:"input"`
	CachedInput   int `json:"cached_input,omitempty"`
	Output        int `json:"output"`
	Reasoning     int `json:"reasoning,omitempty"`
	Context       int `json:"context"`
	ContextWindow int `json:"context_window,omitempty"`
}

// Timing is what one model step cost in wall clock, measured at the endpoint
// rather than derived from entry timestamps — those include the Server own
// bookkeeping and would quietly overstate the model.
type Timing struct {
	// FirstTokenMS is how long the endpoint took to produce anything at all.
	// Zero when the endpoint answered with one document instead of a stream,
	// which is a supported shape with no first token to measure.
	FirstTokenMS int  `json:"first_token_ms,omitempty"`
	ElapsedMS    int  `json:"elapsed_ms,omitempty"`
	Streamed     bool `json:"streamed,omitempty"`
}

// Compaction is what one automatic context reduction did.
//
// It is in the trail because changing what the model can still see can change
// later answers, and a reader who cannot tell a summarized conversation from a
// complete one cannot judge the answers that follow.
type Compaction struct {
	// Method is how the summary was produced. `model_summary` is the checkpoint
	// the model wrote; `summary` is the mechanical one the runtime falls back
	// to when the summarization call cannot be made.
	Method string `json:"method"`
	// Trigger is why it ran: ordinary `pressure`, or a request the endpoint
	// rejected as `context_overflow`.
	Trigger      string `json:"trigger,omitempty"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	// ThresholdTokens is the pressure line this deployment compacts at,
	// resolved from the configured ratio against the endpoint's context window
	// rather than stored as an absolute anywhere.
	ThresholdTokens int `json:"threshold_tokens"`
	// RetainedTokens is how much of the recent tail survived verbatim, and
	// ContextWindowTokens the window both figures were resolved against.
	RetainedTokens      int `json:"retained_tokens,omitempty"`
	ContextWindowTokens int `json:"context_window_tokens,omitempty"`
	// ShadowedFrom and ShadowedTo are the inclusive span of trail sequences
	// this summary replaced. Recording the span rather than implying "everything
	// before me" is what lets the recent tail survive compaction with its exact
	// text: rebuilding the model surface skips the span and keeps what follows.
	ShadowedFrom int32 `json:"shadowed_from"`
	ShadowedTo   int32 `json:"shadowed_to"`
}

const (
	CompactionModelSummary = "model_summary"
	CompactionSummary      = "summary"

	CompactionTriggerPressure = "pressure"
	CompactionTriggerOverflow = "context_overflow"
)

func (compaction *Compaction) valid() bool {
	if compaction == nil {
		return false
	}
	switch compaction.Method {
	case CompactionModelSummary, CompactionSummary:
	default:
		return false
	}
	if compaction.ShadowedFrom < 1 || compaction.ShadowedTo < compaction.ShadowedFrom {
		return false
	}
	return compaction.BeforeTokens > 0 && compaction.AfterTokens > 0 &&
		compaction.AfterTokens < compaction.BeforeTokens &&
		compaction.ThresholdTokens > 0
}

// EvidenceKind separates the things a conclusion can point at. Each one
// resolves to a view ZKE already has, which is what "click through and check it
// yourself" means in practice.
type EvidenceKind string

const (
	EvidenceResource EvidenceKind = "resource"
	EvidenceEvent    EvidenceKind = "event"
	EvidenceMetric   EvidenceKind = "metric"
	EvidenceLog      EvidenceKind = "log"
	// EvidenceHelmRelease is a Helm release, which is not a Kubernetes object
	// and so cannot be a resource reference: it has no GVK, and it is read out
	// of a Secret rather than out of the resource path. Its own kind is also
	// what lets the permission recheck ask the right question — a release
	// reference answers to `cluster.secret.read`, and rechecking it as a
	// resource would re-open it on `cluster.read` alone.
	EvidenceHelmRelease EvidenceKind = "helm_release"
)

// Evidence is one reference to something that really happened.
//
// The fields are a union across the kinds: a resource reference carries
// the object and the resourceVersion it was read at, a metric reference carries
// a catalogue query and its parameters so the chart can be replayed exactly,
// and a log reference carries the container and the window it was taken from.
type Evidence struct {
	Kind    EvidenceKind `json:"kind"`
	Cluster string       `json:"cluster"`
	// TenantID and ProjectID are response-time navigation hints resolved from
	// Cluster identity. Callers cannot supply them as authority, and no
	// authorization decision relies on them.
	TenantID  string `json:"tenant_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	GVK       string `json:"gvk,omitempty"`
	Name      string `json:"name,omitempty"`
	// ResourceVersion is the version an object was read at, so a later reader
	// can tell whether they are looking at the same thing the turn saw.
	ResourceVersion string `json:"resource_version,omitempty"`
	// Query is a metrics catalogue query ID, never an expression: AIOps has no
	// more access to free PromQL than a chart does.
	Query      string    `json:"query,omitempty"`
	Parameters string    `json:"parameters,omitempty"`
	Container  string    `json:"container,omitempty"`
	From       time.Time `json:"from,omitzero"`
	To         time.Time `json:"to,omitzero"`
}

// What one entry may carry. The database refuses a whole entry above 64 KiB;
// these cut each part well below that and mark the entry, so the constraint is
// a backstop rather than the thing operators meet.
//
// A trail is a record of what happened, not a copy of everything that was
// looked at. The evidence reference is what leads back to the full object, the
// full log window or the exact chart, all of which ZKE can already show.
const (
	// maxTextBytes is what one entry body may carry. Large enough for a
	// compaction checkpoint and for a tool result that kept both its head and
	// its tail, and still far enough below the row's own 64 KiB limit that
	// evidence and arguments fit beside it.
	maxTextBytes      = 32 * 1024
	maxArgumentsBytes = 4 * 1024
	maxEvidenceItems  = 32
	// maxSubtaskGoalBytes bounds the one piece of a subtask stamp that is not
	// Server-generated. The goal is written by the model, so it is bounded here
	// as well as by the tool schema rather than trusted to arrive short.
	maxSubtaskGoalBytes = 1024
	// maxTitleBytes mirrors the column. A title is a label in a list, not a
	// summary.
	maxTitleBytes = 200
)

// bound cuts a body down to what an entry may carry and reports whether it had
// to. Cutting happens on a rune boundary: half a character is not a shorter
// string, it is a broken one.
func bound(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := value[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// TitleFrom turns a question into a session title.
//
// Here rather than in the caller because every entry point into a session — the
// App, an in-place invocation from a chart — needs the same answer, and a title
// that differs by where the session was opened from is a list nobody can scan.
func TitleFrom(question string) string {
	title := strings.Join(strings.Fields(question), " ")
	bounded, _ := bound(title, maxTitleBytes)
	return strings.TrimSpace(bounded)
}

// normalize applies the invariants an entry has to satisfy however it was
// built, and reports whether anything was cut.
func (content *Content) normalize(kind Kind) bool {
	truncated := false
	if text, cut := bound(content.Text, maxTextBytes); cut {
		content.Text, truncated = text, true
	} else {
		content.Text = text
	}
	if arguments, cut := bound(content.Arguments, maxArgumentsBytes); cut {
		content.Arguments, truncated = arguments, true
	} else {
		content.Arguments = arguments
	}
	if len(content.Evidence) > maxEvidenceItems {
		content.Evidence = content.Evidence[:maxEvidenceItems]
		truncated = true
	}
	if content.Subtask != nil {
		if goal, cut := bound(content.Subtask.Goal, maxSubtaskGoalBytes); cut {
			content.Subtask.Goal, truncated = goal, true
		}
	}
	// Cluster content is untrusted by construction, not by whoever remembered
	// to say so.
	if kind == KindContext {
		content.Untrusted = true
	}
	return truncated
}
