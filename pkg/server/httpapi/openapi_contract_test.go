package httpapi

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"gopkg.in/yaml.v3"
)

var ginPathParameter = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

func TestHandlersUseSharedJSONResponseWriter(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "JSON" {
				return true
			}
			position := fileSet.Position(call.Pos())
			t.Errorf(
				"%s:%d writes JSON directly; use the shared response writer",
				path,
				position.Line,
			)
			return true
		})
	}
}

func TestOpenAPIContractCoversRegisteredRoutes(t *testing.T) {
	t.Parallel()

	contractPath := filepath.Join(
		"..", "..", "..", "api", "openapi", "zke-server.v1.yaml",
	)
	contents, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		OpenAPI string                            `yaml:"openapi"`
		Paths   map[string]map[string]interface{} `yaml:"paths"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
	}
	if err := yaml.Unmarshal(contents, &contract); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	if contract.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", contract.OpenAPI)
	}
	if len(contract.Servers) != 1 || contract.Servers[0].URL != "/" {
		t.Fatalf("OpenAPI server must use the same-origin relative URL")
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse OpenAPI document tree: %v", err)
	}
	validateOpenAPIReferences(t, document, document)
	engine, ok := testRouter(func(_ context.Context) error { return nil }).(*gin.Engine)
	if !ok {
		t.Fatal("test router is not a Gin engine")
	}
	registered := make(map[string]struct{})
	for _, route := range engine.Routes() {
		path := ginPathParameter.ReplaceAllString(route.Path, `{$1}`)
		registered[strings.ToUpper(route.Method)+" "+path] = struct{}{}
	}
	documented := make(map[string]struct{})
	operationIDs := make(map[string]string)
	for path, item := range contract.Paths {
		for method, rawOperation := range item {
			upperMethod := strings.ToUpper(method)
			switch upperMethod {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				continue
			}
			documented[upperMethod+" "+path] = struct{}{}
			operation, ok := rawOperation.(map[string]interface{})
			if !ok {
				t.Fatalf("%s %s operation is not an object", upperMethod, path)
			}
			operationID, _ := operation["operationId"].(string)
			if operationID == "" {
				t.Errorf("%s %s has no operationId", upperMethod, path)
				continue
			}
			if previous, exists := operationIDs[operationID]; exists {
				t.Errorf(
					"operationId %q is shared by %s and %s %s",
					operationID,
					previous,
					upperMethod,
					path,
				)
			}
			operationIDs[operationID] = upperMethod + " " + path
			validateOperationResponses(t, upperMethod, path, operation)
		}
	}
	validateEnvelopeSchemas(t, document)
	for route := range registered {
		if _, exists := documented[route]; !exists {
			t.Errorf("registered route %q is missing from OpenAPI", route)
		}
	}
	for route := range documented {
		if _, exists := registered[route]; !exists {
			t.Errorf("OpenAPI operation %q has no registered route", route)
		}
	}
	validatePhaseOneFrontendContract(t, contract.Paths)
}

func validateOperationResponses(
	t *testing.T,
	method string,
	path string,
	operation map[string]interface{},
) {
	t.Helper()
	responses, ok := operation["responses"].(map[string]interface{})
	if !ok {
		t.Errorf("%s %s has no responses", method, path)
		return
	}
	hasSuccess := false
	hasClientError := false
	for status := range responses {
		isSuccess := strings.HasPrefix(status, "2") || status == "101"
		hasSuccess = hasSuccess || isSuccess
		hasClientError = hasClientError || strings.HasPrefix(status, "4")
		if strings.HasPrefix(status, "2") {
			validateSuccessResponseEnvelope(t, method, path, status, responses[status])
		}
	}
	if !hasSuccess {
		t.Errorf("%s %s has no success response", method, path)
	}
	if path != "/healthz" && path != "/readyz" && !hasClientError {
		t.Errorf("%s %s has no documented client error", method, path)
	}
	if operationUsesSecurityScheme(operation, "sessionCookie") {
		if _, exists := responses["401"]; !exists {
			t.Errorf("%s %s has no documented unauthenticated response", method, path)
		}
		if method != http.MethodGet {
			if _, exists := responses["403"]; !exists {
				t.Errorf("%s %s has no documented CSRF or forbidden response", method, path)
			}
		}
	}
}

func validatePhaseOneFrontendContract(
	t *testing.T,
	paths map[string]map[string]interface{},
) {
	t.Helper()
	assertSchemaReference(
		t,
		successDataSchema(t, operationAt(t, paths, "/api/v1/setup", "get"), "200"),
		nil,
		"#/components/schemas/SetupStatus",
	)
	assertSchemaReference(
		t,
		operationAt(t, paths, "/api/v1/setup", "post"),
		[]string{"requestBody", "content", "application/json", "schema"},
		"#/components/schemas/SetupRequest",
	)
	assertSchemaReference(
		t,
		successDataSchema(
			t,
			operationAt(t, paths, "/api/v1/auth/me", "get"),
			"200",
		),
		nil,
		"#/components/schemas/CurrentSession",
	)
	assertSchemaReference(
		t,
		operationAt(t, paths, "/api/v1/auth/password", "post"),
		[]string{"requestBody", "content", "application/json", "schema"},
		"#/components/schemas/ChangePasswordRequest",
	)
	agentEnrollment := operationAt(t, paths, "/agent-api/v1/enroll", "post")
	assertSchemaReference(
		t,
		agentEnrollment,
		[]string{"requestBody", "content", "application/json", "schema"},
		"#/components/schemas/AgentEnrollmentRequest",
	)
	for _, status := range []string{"200", "201"} {
		assertSchemaReference(
			t,
			successDataSchema(t, agentEnrollment, status),
			nil,
			"#/components/schemas/AgentEnrollmentResponse",
		)
	}

	listOperations := []struct {
		path   string
		method string
	}{
		{"/api/v1/users", "get"},
		{"/api/v1/role-bindings", "get"},
		{"/api/v1/tenants", "get"},
		{"/api/v1/tenants/{tenant_id}/projects", "get"},
		{"/api/v1/projects/{project_id}/clusters", "get"},
		{"/api/v1/projects/{project_id}/cluster-enrollments", "get"},
	}
	for _, listOperation := range listOperations {
		operation := operationAt(
			t, paths, listOperation.path, listOperation.method,
		)
		for _, reference := range []string{
			"#/components/parameters/ListLimit",
			"#/components/parameters/ListOffset",
			"#/components/parameters/ListSearch",
		} {
			if !operationHasParameterReference(operation, reference) {
				t.Errorf(
					"%s %s is missing parameter %s",
					strings.ToUpper(listOperation.method),
					listOperation.path,
					reference,
				)
			}
		}
		assertSchemaReference(
			t,
			successDataSchema(t, operation, "200"),
			[]string{"properties", "pagination"},
			"#/components/schemas/Pagination",
		)
	}
}

func validateSuccessResponseEnvelope(
	t *testing.T,
	method string,
	path string,
	status string,
	rawResponse interface{},
) {
	t.Helper()
	response, ok := rawResponse.(map[string]interface{})
	if !ok {
		return
	}
	content, _ := response["content"].(map[string]interface{})
	if _, exists := content["application/json"]; !exists {
		return
	}
	operation := map[string]interface{}{
		"responses": map[string]interface{}{status: response},
	}
	if successDataSchema(t, operation, status) == nil {
		t.Errorf(
			"%s %s response %s has no data schema",
			method,
			path,
			status,
		)
	}
}

func successDataSchema(
	t *testing.T,
	operation map[string]interface{},
	status string,
) map[string]interface{} {
	t.Helper()
	responses, ok := operation["responses"].(map[string]interface{})
	if !ok {
		t.Errorf("operation has no responses")
		return nil
	}
	response, ok := responses[status].(map[string]interface{})
	if !ok {
		t.Errorf("response %s is not an object", status)
		return nil
	}
	content, _ := response["content"].(map[string]interface{})
	jsonContent, ok := content["application/json"].(map[string]interface{})
	if !ok {
		t.Errorf("response %s has no application/json content", status)
		return nil
	}
	schema, _ := jsonContent["schema"].(map[string]interface{})
	allOf, ok := schema["allOf"].([]interface{})
	if !ok || len(allOf) != 2 {
		t.Errorf("response %s does not use the success envelope", status)
		return nil
	}
	base, _ := allOf[0].(map[string]interface{})
	if base["$ref"] != "#/components/schemas/SuccessResponse" {
		t.Errorf("response %s has invalid success envelope base", status)
	}
	extension, _ := allOf[1].(map[string]interface{})
	properties, _ := extension["properties"].(map[string]interface{})
	data, ok := properties["data"].(map[string]interface{})
	if !ok {
		t.Errorf("response %s success envelope has no data property", status)
		return nil
	}
	return data
}

func validateEnvelopeSchemas(
	t *testing.T,
	document map[string]interface{},
) {
	t.Helper()
	assertSchemaRequiredProperties(
		t,
		document,
		"SuccessResponse",
		"code",
		"message",
	)
	assertSchemaRequiredProperties(
		t,
		document,
		"Error",
		"code",
		"message",
		"data",
	)
	assertSchemaRequiredProperties(
		t,
		document,
		"ErrorData",
		"error_code",
		"request_id",
	)
}

func assertSchemaRequiredProperties(
	t *testing.T,
	document map[string]interface{},
	name string,
	expected ...string,
) {
	t.Helper()
	components, _ := document["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]interface{})
	schema, ok := schemas[name].(map[string]interface{})
	if !ok {
		t.Errorf("OpenAPI schema %s does not exist", name)
		return
	}
	required, _ := schema["required"].([]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	for _, property := range expected {
		if _, exists := properties[property]; !exists {
			t.Errorf("OpenAPI schema %s has no %s property", name, property)
		}
		found := false
		for _, item := range required {
			found = found || item == property
		}
		if !found {
			t.Errorf("OpenAPI schema %s does not require %s", name, property)
		}
	}
}

func operationAt(
	t *testing.T,
	paths map[string]map[string]interface{},
	path string,
	method string,
) map[string]interface{} {
	t.Helper()
	item, exists := paths[path]
	if !exists {
		t.Fatalf("OpenAPI path %s does not exist", path)
	}
	operation, ok := item[method].(map[string]interface{})
	if !ok {
		t.Fatalf("OpenAPI operation %s %s does not exist", strings.ToUpper(method), path)
	}
	return operation
}

func operationUsesSecurityScheme(
	operation map[string]interface{},
	scheme string,
) bool {
	requirements, _ := operation["security"].([]interface{})
	for _, rawRequirement := range requirements {
		requirement, _ := rawRequirement.(map[string]interface{})
		if _, exists := requirement[scheme]; exists {
			return true
		}
	}
	return false
}

func operationHasParameterReference(
	operation map[string]interface{},
	reference string,
) bool {
	parameters, _ := operation["parameters"].([]interface{})
	for _, rawParameter := range parameters {
		parameter, _ := rawParameter.(map[string]interface{})
		if parameter["$ref"] == reference {
			return true
		}
	}
	return false
}

func assertSchemaReference(
	t *testing.T,
	operation map[string]interface{},
	path []string,
	want string,
) {
	t.Helper()
	var current interface{} = operation
	for _, part := range path {
		object, ok := current.(map[string]interface{})
		if !ok {
			t.Errorf("OpenAPI value at %s is not an object", strings.Join(path, "."))
			return
		}
		current, ok = object[part]
		if !ok {
			t.Errorf("OpenAPI value at %s does not exist", strings.Join(path, "."))
			return
		}
	}
	schema, ok := current.(map[string]interface{})
	if !ok || schema["$ref"] != want {
		t.Errorf("OpenAPI schema reference at %s = %#v, want %s",
			strings.Join(path, "."),
			current,
			want,
		)
	}
}

func validateOpenAPIReferences(
	t *testing.T,
	root map[string]interface{},
	value interface{},
) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Errorf("unsupported OpenAPI reference %#v", child)
					continue
				}
				if !openAPIReferenceExists(root, reference) {
					t.Errorf("OpenAPI reference %q does not exist", reference)
				}
				continue
			}
			validateOpenAPIReferences(t, root, child)
		}
	case []interface{}:
		for _, child := range typed {
			validateOpenAPIReferences(t, root, child)
		}
	}
}

func openAPIReferenceExists(
	root map[string]interface{},
	reference string,
) bool {
	var current interface{} = root
	for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		current, ok = object[part]
		if !ok {
			return false
		}
	}
	return true
}

// TestOpenAPIEnumsMatchServerVocabularies keeps the contract's closed value
// lists tied to the Go definitions they describe.
//
// The route check above proves every endpoint is documented; it says nothing
// about the values those endpoints exchange. Permissions, roles and audit action
// groups are each defined once in Go and transcribed here by hand, and the
// Console's types are generated from this file — so a vocabulary extended in Go
// but not in the contract reaches the Console as a type that silently omits the
// new value, with nothing failing in between.
func TestOpenAPIEnumsMatchServerVocabularies(t *testing.T) {
	t.Parallel()

	document := loadOpenAPIDocument(t)
	schemas := openAPIObject(t, document, "components", "schemas")

	permissions := make([]string, 0, len(rbac.Permissions()))
	for _, permission := range rbac.Permissions() {
		permissions = append(permissions, string(permission))
	}
	assertOpenAPIEnum(
		t,
		"Capability.permissions",
		openAPIEnum(t, schemas, "Capability", "properties", "permissions", "items"),
		permissions,
	)

	groups := make([]string, 0)
	seenGroups := make(map[string]struct{})
	for _, action := range auditaction.All() {
		if _, exists := seenGroups[action.Group]; exists {
			continue
		}
		seenGroups[action.Group] = struct{}{}
		groups = append(groups, action.Group)
	}
	assertOpenAPIEnum(
		t,
		"AuditAction.group",
		openAPIEnum(t, schemas, "AuditAction", "properties", "group"),
		groups,
	)

	assertOpenAPIEnum(
		t,
		"AuditTargetType",
		openAPIEnum(t, schemas, "AuditTargetType"),
		auditaction.TargetTypes(),
	)

	// Roles are data, so no schema may pin them to a list.
	//
	// This check used to say the opposite: every `role` property had to enumerate
	// the two roles the Server compiled in. Once an operator can define a role,
	// an enum here is not documentation but a client that rejects it — the
	// Console's types are generated from this file, so a role created through the
	// API would arrive as a value its own types say cannot exist.
	roleProperties := 0
	for name, rawSchema := range schemas {
		schema, ok := rawSchema.(map[string]interface{})
		if !ok {
			continue
		}
		properties, ok := schema["properties"].(map[string]interface{})
		if !ok {
			continue
		}
		role, ok := properties["role"].(map[string]interface{})
		if !ok {
			continue
		}
		roleProperties++
		if _, enumerated := enumValues(role); enumerated {
			t.Errorf(
				"%s.role enumerates role names; roles are operator-defined and cannot be a closed list",
				name,
			)
		}
	}
	if roleProperties == 0 {
		t.Error("no schema carries a role property; the check proves nothing")
	}
}

func loadOpenAPIDocument(t *testing.T) map[string]interface{} {
	t.Helper()

	contents, err := os.ReadFile(
		filepath.Join("..", "..", "..", "api", "openapi", "zke-server.v1.yaml"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse OpenAPI document: %v", err)
	}
	return document
}

func openAPIObject(
	t *testing.T,
	document map[string]interface{},
	path ...string,
) map[string]interface{} {
	t.Helper()

	current := document
	for _, segment := range path {
		next, ok := current[segment].(map[string]interface{})
		if !ok {
			t.Fatalf("OpenAPI path %s is missing at %q", strings.Join(path, "."), segment)
		}
		current = next
	}
	return current
}

func openAPIEnum(
	t *testing.T,
	schemas map[string]interface{},
	path ...string,
) []string {
	t.Helper()

	node := openAPIObject(t, schemas, path...)
	values, ok := enumValues(node)
	if !ok {
		t.Fatalf("OpenAPI %s has no enum", strings.Join(path, "."))
	}
	return values
}

func enumValues(node map[string]interface{}) ([]string, bool) {
	raw, ok := node["enum"].([]interface{})
	if !ok {
		return nil, false
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

// Compared as sets: the contract is free to order its enum for readability, but
// it may not hold a value the Server does not, or miss one the Server does.
func assertOpenAPIEnum(t *testing.T, name string, documented []string, defined []string) {
	t.Helper()

	documentedSet := make(map[string]struct{}, len(documented))
	for _, value := range documented {
		documentedSet[value] = struct{}{}
	}
	definedSet := make(map[string]struct{}, len(defined))
	for _, value := range defined {
		definedSet[value] = struct{}{}
	}
	for value := range definedSet {
		if _, exists := documentedSet[value]; !exists {
			t.Errorf("%s: %q is defined in Go but missing from the contract", name, value)
		}
	}
	for value := range documentedSet {
		if _, exists := definedSet[value]; !exists {
			t.Errorf("%s: %q is in the contract but not defined in Go", name, value)
		}
	}
}
