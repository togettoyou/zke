package agentprotocol

import (
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

func validHelmInstall() *agentv1.HelmRequest {
	return &agentv1.HelmRequest{
		Action:      agentv1.HelmAction_HELM_ACTION_INSTALL,
		Namespace:   "shop",
		ReleaseName: "checkout",
		ChartSize:   4096,
	}
}

func TestValidateHelmRequestAcceptsEachAction(t *testing.T) {
	t.Parallel()

	cases := map[string]*agentv1.HelmRequest{
		"install": validHelmInstall(),
		"upgrade": {
			Action:      agentv1.HelmAction_HELM_ACTION_UPGRADE,
			Namespace:   "shop",
			ReleaseName: "checkout",
			ChartSize:   4096,
			ValuesSize:  128,
			ReuseValues: true,
		},
		"rollback": {
			Action:      agentv1.HelmAction_HELM_ACTION_ROLLBACK,
			Namespace:   "shop",
			ReleaseName: "checkout",
			Revision:    3,
		},
		"uninstall": {
			Action:      agentv1.HelmAction_HELM_ACTION_UNINSTALL,
			Namespace:   "shop",
			ReleaseName: "checkout",
			KeepHistory: true,
		},
	}
	for name, request := range cases {
		if err := ValidateHelmRequest(request); err != nil {
			t.Errorf("%s: ValidateHelmRequest() = %v, want nil", name, err)
		}
	}
}

// A request whose fields contradict its action is a sign the two sides disagree
// about what is being asked, and is refused rather than half-honoured.
func TestValidateHelmRequestRejectsContradictoryFields(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*agentv1.HelmRequest){
		"install without a chart":       func(r *agentv1.HelmRequest) { r.ChartSize = 0 },
		"install reusing values":        func(r *agentv1.HelmRequest) { r.ReuseValues = true },
		"install naming a revision":     func(r *agentv1.HelmRequest) { r.Revision = 2 },
		"install keeping history":       func(r *agentv1.HelmRequest) { r.KeepHistory = true },
		"unspecified action":            func(r *agentv1.HelmRequest) { r.Action = agentv1.HelmAction_HELM_ACTION_UNSPECIFIED },
		"empty namespace":               func(r *agentv1.HelmRequest) { r.Namespace = "" },
		"namespace with a path segment": func(r *agentv1.HelmRequest) { r.Namespace = "shop/prod" },
		"untrimmed description":         func(r *agentv1.HelmRequest) { r.Description = " deploy " },
		"timeout past the ceiling": func(r *agentv1.HelmRequest) {
			r.TimeoutSeconds = helmrelease.MaxTimeoutSeconds + 1
		},
		"history past the ceiling": func(r *agentv1.HelmRequest) {
			r.MaxHistory = helmrelease.MaxHistoryLimit + 1
		},
	}
	for name, mutate := range cases {
		request := validHelmInstall()
		mutate(request)
		if err := ValidateHelmRequest(request); err == nil {
			t.Errorf("%s: ValidateHelmRequest() = nil, want a refusal", name)
		}
	}
}

func TestValidateHelmRequestRejectsUpgradeValueModeConflict(t *testing.T) {
	t.Parallel()

	request := validHelmInstall()
	request.Action = agentv1.HelmAction_HELM_ACTION_UPGRADE
	request.ResetValues = true
	request.ReuseValues = true
	if err := ValidateHelmRequest(request); err == nil {
		t.Fatal("ValidateHelmRequest() accepted reset_values together with reuse_values")
	}
}

// A rollback replays what Helm already stored. A chart or a values document
// travelling with one means the caller thinks it is choosing the content.
func TestValidateHelmRequestRejectsRollbackContent(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*agentv1.HelmRequest){
		"with a chart":  func(r *agentv1.HelmRequest) { r.ChartSize = 1024 },
		"with values":   func(r *agentv1.HelmRequest) { r.ValuesSize = 16 },
		"creating a ns": func(r *agentv1.HelmRequest) { r.CreateNamespace = true },
	} {
		request := &agentv1.HelmRequest{
			Action:      agentv1.HelmAction_HELM_ACTION_ROLLBACK,
			Namespace:   "shop",
			ReleaseName: "checkout",
		}
		mutate(request)
		if err := ValidateHelmRequest(request); err == nil {
			t.Errorf("rollback %s: ValidateHelmRequest() = nil, want a refusal", name)
		}
	}
}

func TestValidateHelmRequestBoundsBodies(t *testing.T) {
	t.Parallel()

	oversizedChart := validHelmInstall()
	oversizedChart.ChartSize = helmrelease.MaxChartBytes + 1
	if err := ValidateHelmRequest(oversizedChart); !errors.Is(err, ErrStreamBodyTooLarge) {
		t.Fatalf("oversized chart: ValidateHelmRequest() = %v, want ErrStreamBodyTooLarge", err)
	}
	oversizedValues := validHelmInstall()
	oversizedValues.ValuesSize = helmrelease.MaxValuesBytes + 1
	if err := ValidateHelmRequest(oversizedValues); !errors.Is(err, ErrStreamBodyTooLarge) {
		t.Fatalf("oversized values: ValidateHelmRequest() = %v, want ErrStreamBodyTooLarge", err)
	}
}

// Helm caps a release name at 53 characters because it derives object names
// from it. A name Kubernetes would accept but Helm would not must be refused
// here, not by a rendering failure in the Cluster.
func TestValidHelmReleaseName(t *testing.T) {
	t.Parallel()

	accepted := []string{"a", "checkout", "checkout-v2", "team.checkout", "c1"}
	for _, name := range accepted {
		if !validHelmReleaseName(name) {
			t.Errorf("validHelmReleaseName(%q) = false, want true", name)
		}
	}
	refused := []string{
		"",
		"Checkout",
		"-checkout",
		"checkout-",
		".checkout",
		"checkout.",
		"check--out.",
		"check..out",
		"check-.out",
		"check_out",
		strings.Repeat("a", 54),
	}
	for _, name := range refused {
		if validHelmReleaseName(name) {
			t.Errorf("validHelmReleaseName(%q) = true, want false", name)
		}
	}
}

// A failure that explains nothing leaves the Server with an empty box to render,
// so the response validation refuses one.
func TestValidateHelmResponseRequiresAFailureReason(t *testing.T) {
	t.Parallel()

	silent := &agentv1.HelmResponse{Result: agentv1.ResultCode_RESULT_CODE_INTERNAL}
	if err := validateHelmResponse(silent, true); err == nil {
		t.Fatal("validateHelmResponse() accepted a failure with no reason")
	}
	explained := &agentv1.HelmResponse{
		Result:  agentv1.ResultCode_RESULT_CODE_INTERNAL,
		Reason:  "HelmOperationFailed",
		Message: "rendering failed",
	}
	if err := validateHelmResponse(explained, true); err != nil {
		t.Fatalf("validateHelmResponse() = %v, want nil", err)
	}
}

func TestValidateHelmResponseBoundsTheReport(t *testing.T) {
	t.Parallel()

	response := &agentv1.HelmResponse{
		Result:   agentv1.ResultCode_RESULT_CODE_OK,
		BodySize: helmrelease.MaxReportBytes + 1,
	}
	if err := validateHelmResponse(response, true); !errors.Is(err, ErrStreamBodyTooLarge) {
		t.Fatalf("validateHelmResponse() = %v, want ErrStreamBodyTooLarge", err)
	}
}
