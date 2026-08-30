package airuntime

import (
	"context"
	"encoding/json"

	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// ToolSpec is one tool the model may choose to call.
//
// The catalogue is owned by the Server, not by the session and not by the
// model: every entry here has to keep obeying Server + Agent transport, RBAC,
// the session fixed Cluster and the audit trail, and a tool a conversation
// could install would be a way around all four. What the model gets to decide
// is which of these to call, with what arguments, and in what order.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is the JSON Schema object advertised to the model. It is also what
	// the implementation validates against, so the contract the model reads is
	// the contract it is held to.
	Schema json.RawMessage
	// Target extracts the namespace and object identity used in approval,
	// trajectory and audit records before the tool executes. The runtime always
	// overwrites Cluster with the session workspace. Nil is valid for tools whose
	// target is only the Cluster or cannot be known from their arguments.
	Target func(json.RawMessage) *aisession.Target
	// Permissions are rechecked for the calling operator before every single
	// invocation, all of them. A tool that reads an object and its Events needs
	// both, and granting it on the strength of one would let a permission the
	// operator does have stand in for one they do not. `ai.run` opens AIOps; it
	// never stands in for any of these.
	Permissions []rbac.Permission
	// ConditionalPermissions are selected and rechecked by the tool from its
	// resolved target or documents. They are advertised for operator clarity but
	// are not all required at once.
	ConditionalPermissions []rbac.Permission
	// Sensitive marks a tool that stops for a person unless the session runs in
	// full mode. It mirrors what ZKE already makes an operator confirm rather
	// than inventing a second notion of risky.
	Sensitive bool
	// SensitiveWhen upgrades a particular call to sensitive from its arguments.
	// This is used by tools such as manifest Apply: an ordinary Deployment is a
	// normal write, while RBAC or a protected Namespace in the same tool must
	// still stop in assisted mode. It may only make a call more restrictive.
	SensitiveWhen func(json.RawMessage) bool
	// Mutating marks a tool that changes a cluster. The loop uses it for
	// approval and to serialize every call in the same model step.
	Mutating bool
}

// ToolInvocation is one authorized call, already fixed to the session Cluster.
type ToolInvocation struct {
	Name string
	// TurnID is the runtime-owned identity of the current AIOps turn. It is
	// stable across every step in that turn, opaque outside the Server and never
	// accepted from model arguments. Turn-scoped tools use it to reuse bounded
	// resources without allowing one conversation turn to reach another.
	TurnID string
	// ClusterID is the session workspace. The runtime sets it; a tool may not
	// take a target cluster from the model.
	ClusterID string
	// UserID is the operator the call runs as. The runtime has already checked
	// Permissions against it; a tool needs it for the scoping some read paths
	// do on their own, never as a second authorization of its own devising.
	UserID    string
	Arguments json.RawMessage
	// IdempotencyKey is a stable identity for this exact model-requested call.
	// The runtime derives it from the session, turn, step and call identifier;
	// mutating tools pass it into the existing Server -> Agent write path so a
	// lost response cannot turn a retry into a second change.
	IdempotencyKey string
}

// ToolResult is what one call produced.
//
// Text is cluster content and therefore untrusted data. Evidence is what makes
// the result checkable: each item resolves to a view ZKE already has, so a
// reader can go and look at the same object rather than take the summary on
// trust.
type ToolResult struct {
	Text     string
	Evidence []aisession.Evidence
	Target   *aisession.Target
	// AuditTargets gives a multi-object tool one deployment-audit row per
	// resolved object. The trajectory still holds one bounded tool result, while
	// the audit log can answer exactly which objects a manifest reached.
	AuditTargets []ToolAuditTarget
	// Failed keeps a structured tool answer (for example a per-document manifest
	// plan) while telling the loop that the requested outcome did not complete.
	Failed bool
	// Denied is a Failed result caused by the tool's document- or target-level
	// authorization. Static catalogue permissions are checked by the runtime;
	// tools whose required permission depends on their contents report it here.
	Denied bool
	// Trusted marks an answer that did not come out of a Cluster and may
	// therefore be read as instruction rather than as data.
	//
	// It exists for exactly one shape of tool: one whose whole answer is text
	// the Server itself ships, such as a skill playbook. Nothing that passed
	// through an Agent, an object, a log or a command may set it, because the
	// rule that cluster content is data and never instruction is what the
	// approval modes are protecting. The default is the safe one: a result says
	// nothing and is recorded as untrusted.
	Trusted bool
	// View is an application the tool asked the Console to open on the
	// operator's desktop. It is written onto the durable tool result, so a
	// desktop that moved is part of the record rather than a live-only nudge.
	View *aisession.View
}

type ToolAuditTarget struct {
	Target            aisession.Target
	Result            string
	MissingPermission string
}

// ToolSet is the catalogue the runtime advertises and calls.
//
// An interface so that the runtime keeps owning authorization, budgets and the
// trail while knowing nothing about Kubernetes: the implementation lives in
// aitools, which owns the read paths and their bounds.
type ToolSet interface {
	Specs() []ToolSpec
	Invoke(context.Context, ToolInvocation) (ToolResult, error)
}

// TurnScopedToolSet owns resources that may be reused by multiple calls in one
// AIOps turn. The runtime calls CloseTurn exactly once when that turn ends,
// including cancellation and failure paths. Implementations must make cleanup
// idempotent and must not rely on the already-cancelled turn context.
type TurnScopedToolSet interface {
	CloseTurn(context.Context, string) error
}

// requiresApproval reports whether a call has to stop for a person under the
// session current mode.
//
// The modes differ only in who presses the button; none of them grants a
// permission the operator does not already have. What they change is how far a
// prompt injection out of a Pod log can reach, which is why the mode in force
// is recorded on every request.
func requiresApproval(spec ToolSpec, mode aisession.ApprovalMode) bool {
	return requiresApprovalFor(spec, mode, nil)
}

func requiresApprovalFor(
	spec ToolSpec,
	mode aisession.ApprovalMode,
	arguments json.RawMessage,
) bool {
	sensitive := spec.Sensitive
	if spec.SensitiveWhen != nil && spec.SensitiveWhen(arguments) {
		sensitive = true
	}
	switch mode {
	case aisession.ApprovalFull:
		return false
	case aisession.ApprovalAssisted:
		return sensitive
	default:
		return sensitive || spec.Mutating
	}
}

func specNames(specs []ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func findSpec(specs []ToolSpec, name string) (ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}
