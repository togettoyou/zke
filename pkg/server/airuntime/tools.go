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
	// Permissions are rechecked for the calling operator before every single
	// invocation, all of them. A tool that reads an object and its Events needs
	// both, and granting it on the strength of one would let a permission the
	// operator does have stand in for one they do not. `ai.run` opens AIOps; it
	// never stands in for any of these.
	Permissions []rbac.Permission
	// Sensitive marks a tool that stops for a person unless the session runs in
	// full mode. It mirrors what ZKE already makes an operator confirm rather
	// than inventing a second notion of risky.
	Sensitive bool
	// Mutating marks a tool that changes a cluster. No tool in the shipped
	// catalogue sets it; the loop honours it so that adding one is a
	// registration rather than a redesign of the approval path.
	Mutating bool
}

// ToolInvocation is one authorized call, already fixed to the session Cluster.
type ToolInvocation struct {
	Name string
	// ClusterID is the session workspace. The runtime sets it; a tool may not
	// take a target cluster from the model.
	ClusterID string
	// UserID is the operator the call runs as. The runtime has already checked
	// Permissions against it; a tool needs it for the scoping some read paths
	// do on their own, never as a second authorization of its own devising.
	UserID    string
	Arguments json.RawMessage
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

// requiresApproval reports whether a call has to stop for a person under the
// session current mode.
//
// The modes differ only in who presses the button; none of them grants a
// permission the operator does not already have. What they change is how far a
// prompt injection out of a Pod log can reach, which is why the mode in force
// is recorded on every request.
func requiresApproval(spec ToolSpec, mode aisession.ApprovalMode) bool {
	switch mode {
	case aisession.ApprovalFull:
		return false
	case aisession.ApprovalAssisted:
		return spec.Sensitive
	default:
		return spec.Sensitive || spec.Mutating
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
