package agentprotocol

import (
	"bytes"
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

// Progress frames and the response travel on one Stream, so the reader has to
// be able to tell them apart and the writer has to stop before the response
// goes out. A frame interleaved with the response would leave the Server
// reading a message where the report body should be.
func TestHelmProgressFramesPrecedeTheResponse(t *testing.T) {
	t.Parallel()

	stream := &bytes.Buffer{}
	writer := newHelmProgressWriter(stream, true)
	writer.Progress("creating 6 resource(s)")
	writer.Progress("   ")
	writer.Progress("beginning wait for 6 resources")
	writer.close()
	writer.Progress("logged after the action returned")
	if err := writeHelmResponse(stream, &agentv1.HelmResponse{
		Result:   agentv1.ResultCode_RESULT_CODE_OK,
		BodySize: 0,
	}, true); err != nil {
		t.Fatalf("writeHelmResponse() = %v", err)
	}

	var seen []string
	response, err := readHelmResponse(stream, true, func(line *agentv1.HelmProgress) {
		seen = append(seen, line.GetMessage())
		if line.GetAtUnixMillis() <= 0 {
			t.Error("a progress line carried no timestamp")
		}
	})
	if err != nil {
		t.Fatalf("readHelmResponse() = %v", err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("response = %+v", response)
	}
	want := []string{"creating 6 resource(s)", "beginning wait for 6 resources"}
	if len(seen) != len(want) {
		t.Fatalf("progress = %q, want %q", seen, want)
	}
	for index, message := range want {
		if seen[index] != message {
			t.Fatalf("progress = %q, want %q", seen, want)
		}
	}
}

// An Agent that never advertised progress writes a bare response, and a Server
// that did not ask for it reads one. The two shapes are chosen by the same
// field, so neither side can be reading what the other is not writing.
func TestHelmResponseWithoutProgressKeepsTheBareShape(t *testing.T) {
	t.Parallel()

	stream := &bytes.Buffer{}
	writer := newHelmProgressWriter(stream, false)
	writer.Progress("this Agent was not asked for progress")
	if stream.Len() != 0 {
		t.Fatalf("a sink that was not enabled wrote %d bytes", stream.Len())
	}
	if err := writeHelmResponse(stream, &agentv1.HelmResponse{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
	}, false); err != nil {
		t.Fatalf("writeHelmResponse() = %v", err)
	}
	response, err := readHelmResponse(stream, false, nil)
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("readHelmResponse() = %+v, %v", response, err)
	}
}

// Helm logs a line per wait poll, so a release that waits out a long timeout
// would otherwise write without bound on a Stream nobody is draining. Past the
// bound the Agent says so once and stops; the response is unaffected.
func TestHelmProgressIsBounded(t *testing.T) {
	t.Parallel()

	stream := &bytes.Buffer{}
	writer := newHelmProgressWriter(stream, true)
	for range maxHelmProgressLines * 2 {
		writer.Progress("waiting")
	}
	writer.close()
	if err := writeHelmResponse(stream, &agentv1.HelmResponse{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
	}, true); err != nil {
		t.Fatalf("writeHelmResponse() = %v", err)
	}
	lines := 0
	last := ""
	if _, err := readHelmResponse(stream, true, func(line *agentv1.HelmProgress) {
		lines++
		last = line.GetMessage()
	}); err != nil {
		t.Fatalf("readHelmResponse() = %v", err)
	}
	if lines != maxHelmProgressLines {
		t.Fatalf("wrote %d progress lines, want %d", lines, maxHelmProgressLines)
	}
	if !strings.Contains(last, "truncated") {
		t.Fatalf("the bound was reached silently: last line = %q", last)
	}
}

// A message longer than the protocol carries is cut rather than refused: a
// Cluster that logged something enormous must not be able to fail an operation
// that is otherwise going fine.
func TestHelmProgressBoundsOneLine(t *testing.T) {
	t.Parallel()

	stream := &bytes.Buffer{}
	writer := newHelmProgressWriter(stream, true)
	writer.Progress(strings.Repeat("x", maxHelmStringLength*2))
	writer.close()
	if err := writeHelmResponse(stream, &agentv1.HelmResponse{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
	}, true); err != nil {
		t.Fatalf("writeHelmResponse() = %v", err)
	}
	if _, err := readHelmResponse(stream, true, func(line *agentv1.HelmProgress) {
		if len(line.GetMessage()) != maxHelmStringLength {
			t.Errorf("line length = %d", len(line.GetMessage()))
		}
	}); err != nil {
		t.Fatalf("readHelmResponse() = %v", err)
	}
}
