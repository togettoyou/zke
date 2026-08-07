package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/rbac"
)

// The routes are the only place that decides at which scope a permission is
// checked, and `rbac.MinimumScope` is a second statement of the same fact —
// consulted when a RoleBinding is written, and when capabilities are reported.
// Two statements of one fact drift, and this one drifts silently: a permission
// that gains a Project-scoped route while still declared global-only makes
// legitimate bindings impossible, and one that loses its Project route without
// its floor being raised goes back to being grantable and inert.
//
// So the map is checked against the routes rather than trusted. `routes.go` is
// parsed instead of the built router because Gin reports a route's method, path
// and handler chain, and nothing about which permission each middleware closed
// over — the fact under test is exactly the one the router does not keep.
func TestPermissionScopeFloorsMatchRoutes(t *testing.T) {
	t.Parallel()

	scopes := permissionScopesInRoutes(t)
	if len(scopes) == 0 {
		t.Fatal("no authorization middleware calls found in routes.go")
	}

	for permission, kinds := range scopes {
		expected := scopeFloorForMiddlewares(kinds)
		if actual := rbac.MinimumScope(rbac.Permission(permission)); actual != expected {
			t.Errorf(
				"%s is enforced at %s, so its floor is %s, but rbac.MinimumScope() = %s",
				permission,
				strings.Join(kinds, ","),
				expected,
				actual,
			)
		}
	}

	// A permission with no fixed-scope route at all — the ones resolved against
	// the caller's visibility inside the service — must carry no floor either:
	// nothing about them is refused for being bound too narrowly.
	for _, permission := range rbac.Permissions() {
		if _, routed := scopes[string(permission)]; routed {
			continue
		}
		if floor := rbac.MinimumScope(permission); floor != "project" {
			t.Errorf(
				"%s has no fixed-scope route but declares the floor %s",
				permission,
				floor,
			)
		}
	}
}

// The floor a set of middlewares implies: the narrowest binding scope that
// satisfies at least one of the routes checking this permission.
//
// `RequireCluster` resolves the Cluster to its owning Project and authorizes
// there, so it is a Project-scope check and imposes no floor.
func scopeFloorForMiddlewares(kinds []string) string {
	floor := "global"
	for _, kind := range kinds {
		switch kind {
		case "RequireTenant":
			if floor == "global" {
				floor = "tenant"
			}
		case "RequireProject", "RequireCluster":
			return "project"
		}
	}
	return floor
}

// Maps each permission named in routes.go to the sorted, deduplicated set of
// authorization middlewares it is passed to.
func permissionScopesInRoutes(t *testing.T) map[string][]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "routes.go", nil, 0)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}

	wireNames := permissionConstantNames(t)
	found := make(map[string]map[string]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector || !strings.HasPrefix(selector.Sel.Name, "Require") {
			return true
		}
		// RequireAuthentication and RequireCSRF take no permission; they simply
		// contribute no arguments of the shape below.
		for _, argument := range call.Args {
			permission, ok := rbacPermissionName(argument, wireNames)
			if !ok {
				continue
			}
			if found[permission] == nil {
				found[permission] = make(map[string]struct{})
			}
			found[permission][selector.Sel.Name] = struct{}{}
		}
		return true
	})

	scopes := make(map[string][]string, len(found))
	for permission, kinds := range found {
		names := make([]string, 0, len(kinds))
		for kind := range kinds {
			names = append(names, kind)
		}
		sort.Strings(names)
		scopes[permission] = names
	}
	return scopes
}

// Resolves `rbac.PermissionX` to the wire name X stands for, so the test
// compares the same strings the rest of the platform uses.
func rbacPermissionName(argument ast.Expr, wireNames map[string]string) (string, bool) {
	selector, isSelector := argument.(*ast.SelectorExpr)
	if !isSelector {
		return "", false
	}
	packageIdent, isIdent := selector.X.(*ast.Ident)
	if !isIdent || packageIdent.Name != "rbac" {
		return "", false
	}
	permission, known := wireNames[selector.Sel.Name]
	return permission, known
}

// Reads the constant name to wire name mapping out of the rbac package's own
// declarations. Parsed rather than exported from rbac: nothing outside a test
// needs to go from a Go identifier back to a permission, and adding an API for
// it would be adding production surface to serve a test.
func permissionConstantNames(t *testing.T) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "../rbac/model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse rbac/model.go: %v", err)
	}

	names := make(map[string]string)
	ast.Inspect(file, func(node ast.Node) bool {
		spec, isValue := node.(*ast.ValueSpec)
		if !isValue || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		literal, isLiteral := spec.Values[0].(*ast.BasicLit)
		if !isLiteral || literal.Kind != token.STRING {
			return true
		}
		names[spec.Names[0].Name] = strings.Trim(literal.Value, `"`)
		return true
	})

	// Every permission the Server defines has to have been found, or the parse
	// silently under-reports and the comparison below passes by knowing nothing.
	for _, permission := range rbac.Permissions() {
		found := false
		for _, wire := range names {
			if wire == string(permission) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("permission %s not found in rbac/model.go declarations", permission)
		}
	}
	return names
}
