// Package auditaction_test is external on purpose. The vocabulary has to be
// checked against `pkg/server/rbac`, and `auditaction` cannot import it: rbac
// depends on the store, and the store depends on auditaction. An external test
// package closes the loop without creating one.
package auditaction_test

import (
	"testing"

	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// Every permission reaches the action column when authorization refuses a
// request, so every permission must be part of the published vocabulary. Adding
// a permission without publishing it would produce denials the Console's filter
// cannot select, and nothing else in the build would notice.
func TestEveryPermissionIsPublishedAsAnAction(t *testing.T) {
	t.Parallel()

	for _, permission := range rbac.Permissions() {
		if !auditaction.Known(string(permission)) {
			t.Errorf(
				"permission %q is recorded on denial but missing from "+
					"auditaction.All(); add it to the denied group",
				permission,
			)
		}
	}
}

// The filter matches on the name alone, so a name declared twice would offer the
// operator two picker entries that select the same events — and, if the groups
// differ, two contradictory claims about which family it belongs to.
func TestActionNamesAreUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]string, len(auditaction.All()))
	for _, action := range auditaction.All() {
		if group, exists := seen[action.Name]; exists {
			t.Errorf(
				"action %q is declared twice, in groups %q and %q",
				action.Name,
				group,
				action.Group,
			)
			continue
		}
		seen[action.Name] = action.Group
	}
}

// The denied group is exactly the permissions that are not already an action
// name. A permission that is also an action stays in the family it acts on, so
// finding it here means the same name is about to be declared twice.
func TestDeniedGroupHoldsOnlyPermissionNames(t *testing.T) {
	t.Parallel()

	permissions := make(map[string]struct{}, len(rbac.Permissions()))
	for _, permission := range rbac.Permissions() {
		permissions[string(permission)] = struct{}{}
	}
	for _, action := range auditaction.All() {
		if action.Group != auditaction.GroupDenied {
			continue
		}
		if _, exists := permissions[action.Name]; !exists {
			t.Errorf(
				"action %q is in the denied group but is not a permission name",
				action.Name,
			)
		}
	}
}
