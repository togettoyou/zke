package httpapi

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Routes that are reachable without a session, with the reason each one is
// safe to expose. Adding an entry here is a deliberate decision to publish an
// endpoint, which is why it must be written down rather than inferred.
var publicRoutes = map[string]string{
	"GET /healthz":                   "liveness probe, returns no data",
	"GET /readyz":                    "readiness probe, returns no data",
	"GET /api/v1/setup":              "reports only whether a global administrator must be configured",
	"POST /api/v1/setup":             "creates the first global administrator only while none exists; source limited and transactionally guarded",
	"POST /api/v1/auth/login":        "establishes the session; rate limited per account and source",
	"POST /agent-api/v1/enroll":      "authenticated by a one-time enrollment token; rate limited per source",
	"GET /agent-install/v1/manifest": "authenticated by an installation token; rate limited per source",
}

// Authenticated routes that carry no Require* middleware, with the reason.
// Either the handler resolves the caller's RBAC visibility itself because the
// answer is the set of resources they may see, or the operation acts on the
// caller's own account and has no scope to authorize against.
var serviceAuthorizedRoutes = map[string]string{
	"GET /api/v1/auth/me":                     "reports the caller's own identity and capabilities",
	"POST /api/v1/auth/logout":                "ends the caller's own session",
	"GET /api/v1/audit-events":                "audit.read visibility is resolved and pushed into the query",
	"GET /api/v1/audit-events/actions":        "the closed action vocabulary the Server writes; describes the system's shape, holds no tenant data",
	"GET /api/v1/events":                      "cluster.read visibility is resolved before the stream opens and re-checked while it runs",
	"GET /api/v1/tenants":                     "tenant.read visibility is resolved and pushed into the query",
	"GET /api/v1/tenants/:tenant_id":          "a Project-scoped user must still see the Tenant holding their Project",
	"GET /api/v1/tenants/:tenant_id/projects": "project.read visibility is resolved and pushed into the query",
}

// TestEveryRouteIsAuthorized reads the route table and holds it to the rules
// registerRoutes documents. Route registration is repetitive and grows with
// every feature, so a missing authorization middleware is exactly the kind of
// defect that survives review; this fails the build instead.
func TestEveryRouteIsAuthorized(t *testing.T) {
	t.Parallel()

	routes := parseRegisteredRoutes(t)
	if len(routes) < 30 {
		t.Fatalf("parsed %d routes, far fewer than the route table holds", len(routes))
	}

	seenPublic := map[string]bool{}
	seenServiceAuthorized := map[string]bool{}

	for _, route := range routes {
		key := route.key()

		if _, public := publicRoutes[key]; public {
			seenPublic[key] = true
			if route.hasAuthentication() {
				t.Errorf("%s is listed as public but requires authentication", key)
			}
			continue
		}

		if !route.hasAuthentication() {
			t.Errorf(
				"%s neither requires authentication nor is listed as public",
				key,
			)
			continue
		}

		// A mutation without CSRF protection is reachable from any origin that
		// can make the browser send the session cookie.
		if route.mutates() && !route.hasCSRF() {
			t.Errorf("%s mutates state without RequireCSRF", key)
		}

		if route.hasAuthorization() {
			if _, listed := serviceAuthorizedRoutes[key]; listed {
				t.Errorf(
					"%s carries Require* middleware but is still listed as service-authorized",
					key,
				)
			}
			continue
		}
		if _, listed := serviceAuthorizedRoutes[key]; !listed {
			t.Errorf(
				"%s carries no Require* middleware and is not listed as service-authorized; "+
					"add the middleware, or list it with the visibility its handler resolves",
				key,
			)
			continue
		}
		seenServiceAuthorized[key] = true
	}

	// A stale exemption is as dangerous as a missing check: it silently keeps
	// covering a route that was renamed or re-scoped.
	for key := range publicRoutes {
		if !seenPublic[key] {
			t.Errorf("public route %s is no longer registered", key)
		}
	}
	for key := range serviceAuthorizedRoutes {
		if !seenServiceAuthorized[key] {
			t.Errorf("service-authorized route %s is no longer registered", key)
		}
	}
}

type registeredRoute struct {
	method     string
	path       string
	middleware []string
}

func (route registeredRoute) key() string {
	return route.method + " " + route.path
}

func (route registeredRoute) mutates() bool {
	return route.method != "GET" && route.method != "HEAD"
}

func (route registeredRoute) has(fragment string) bool {
	return slices.ContainsFunc(route.middleware, func(item string) bool {
		return strings.Contains(item, fragment)
	})
}

func (route registeredRoute) hasAuthentication() bool {
	return route.has("authMiddleware.RequireAuthentication")
}

func (route registeredRoute) hasCSRF() bool {
	return route.has("authMiddleware.RequireCSRF")
}

func (route registeredRoute) hasAuthorization() bool {
	return route.has("authorizationMiddleware.Require")
}

// parseRegisteredRoutes reads routes.go and reconstructs each route with the
// middleware that runs before it, including what its enclosing groups apply.
func parseRegisteredRoutes(t *testing.T) []registeredRoute {
	t.Helper()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "routes.go", nil, 0)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}
	body := registerRoutesBody(t, file)

	render := func(node ast.Node) string {
		var builder strings.Builder
		if err := printer.Fprint(&builder, fileSet, node); err != nil {
			t.Fatalf("render route expression: %v", err)
		}
		return builder.String()
	}

	// Groups are tracked by the variable they are assigned to, so a route
	// registered on a group inherits everything its ancestors apply.
	type routeGroup struct {
		parent     string
		prefix     string
		middleware []string
	}
	groups := map[string]*routeGroup{"router": {}}

	resolve := func(name string) (string, []string, bool) {
		var prefix string
		var middleware []string
		for current := name; current != ""; {
			group, exists := groups[current]
			if !exists {
				return "", nil, false
			}
			prefix = group.prefix + prefix
			middleware = append(append([]string{}, group.middleware...), middleware...)
			current = group.parent
		}
		return prefix, middleware, true
	}

	var routes []registeredRoute
	for _, statement := range body.List {
		switch typed := statement.(type) {
		case *ast.AssignStmt:
			call, receiver, method, ok := methodCall(typed.Rhs)
			if !ok || method != "Group" || len(typed.Lhs) != 1 {
				continue
			}
			name, ok := typed.Lhs[0].(*ast.Ident)
			if !ok {
				continue
			}
			groups[name.Name] = &routeGroup{
				parent: receiver,
				prefix: stringLiteral(call.Args[0]),
			}
		case *ast.ExprStmt:
			call, receiver, method, ok := methodCall([]ast.Expr{typed.X})
			if !ok {
				continue
			}
			if method == "Use" {
				group, exists := groups[receiver]
				if !exists {
					continue
				}
				for _, argument := range call.Args {
					group.middleware = append(group.middleware, render(argument))
				}
				continue
			}
			if !slices.Contains(
				[]string{"GET", "POST", "PUT", "PATCH", "DELETE"},
				method,
			) {
				continue
			}
			prefix, inherited, ok := resolve(receiver)
			if !ok || len(call.Args) == 0 {
				continue
			}
			// The last argument is the handler; everything between the path and
			// it is route-level middleware.
			middleware := append([]string{}, inherited...)
			for _, argument := range call.Args[1 : len(call.Args)-1] {
				middleware = append(middleware, render(argument))
			}
			routes = append(routes, registeredRoute{
				method:     method,
				path:       normalizeRoutePath(prefix + stringLiteral(call.Args[0])),
				middleware: middleware,
			})
		}
	}
	return routes
}

func registerRoutesBody(t *testing.T, file *ast.File) *ast.BlockStmt {
	t.Helper()

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "registerRoutes" {
			return function.Body
		}
	}
	t.Fatal("registerRoutes was not found in routes.go")
	return nil
}

// methodCall reports the receiver identifier and selected method of a single
// call expression such as `userRoutes.POST(...)`.
func methodCall(expressions []ast.Expr) (*ast.CallExpr, string, string, bool) {
	if len(expressions) != 1 {
		return nil, "", "", false
	}
	call, ok := expressions[0].(*ast.CallExpr)
	if !ok {
		return nil, "", "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, "", "", false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil, "", "", false
	}
	return call, receiver.Name, selector.Sel.Name, true
}

func stringLiteral(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}

// normalizeRoutePath collapses the empty path a group uses for its own root.
func normalizeRoutePath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
