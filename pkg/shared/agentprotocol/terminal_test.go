package agentprotocol

import (
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/permissionname"
)

func TestTerminalSessionCreateRequiresSupportedImagePullPolicy(t *testing.T) {
	header := &agentv1.StreamHeader{IdempotencyKey: "terminal-create-request"}
	request := &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE,
		SessionId: "11111111-1111-4111-8111-111111111111",
		UserId:    "22222222-2222-4222-8222-222222222222",
		Namespace: "zke-system", Permissions: []string{permissionname.ClusterTerminalExec},
		TtlSeconds: 60, Image: "registry.example.com/zke-terminal:v1", ImagePullPolicy: "Always",
	}
	if err := validateTerminalSessionRequest(header, request); err != nil {
		t.Fatalf("valid create request: %v", err)
	}
	for _, policy := range []string{"", "Sometimes"} {
		request.ImagePullPolicy = policy
		if err := validateTerminalSessionRequest(header, request); err == nil {
			t.Fatalf("image pull policy %q was accepted", policy)
		}
	}
}

func TestTerminalSessionDeleteRejectsImagePullPolicy(t *testing.T) {
	header := &agentv1.StreamHeader{IdempotencyKey: "terminal-delete-request"}
	request := &agentv1.TerminalSessionRequest{
		Action:    agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE,
		SessionId: "11111111-1111-4111-8111-111111111111",
		UserId:    "22222222-2222-4222-8222-222222222222",
		Namespace: "zke-system", ImagePullPolicy: "Never",
	}
	if err := validateTerminalSessionRequest(header, request); err == nil {
		t.Fatal("delete request with an image pull policy was accepted")
	}
	request.ImagePullPolicy = ""
	if err := validateTerminalSessionRequest(header, request); err != nil {
		t.Fatalf("valid delete request: %v", err)
	}
}
