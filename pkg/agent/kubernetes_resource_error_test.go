package agent

import (
	"net/http"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// The API Server rejecting the submitted object is the one failure whose text
// the caller needs: it names the field and, for a NodePort, the range this
// cluster was configured with — which appears nowhere else.
func TestKubernetesResourceErrorCarriesRejectionText(t *testing.T) {
	t.Parallel()

	invalid := apierrors.NewInvalid(
		schema.GroupKind{Kind: "Service"},
		"test1",
		field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "ports").Index(0).Child("nodePort"),
				38080,
				"provided port is not in the valid range. The range of valid ports is 30000-32767",
			),
		},
	)
	response := kubernetesResourceError(invalid)
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT ||
		response.GetReason() != string(metav1.StatusReasonInvalid) {
		t.Fatalf("unexpected rejection response: %+v", response)
	}
	if !strings.Contains(response.GetMessage(), "30000-32767") {
		t.Fatalf("rejection message = %q, want the API Server's own explanation", response.GetMessage())
	}

	admissionMessage := `admission webhook "vscaledobject.kb.io" denied the request: ` +
		`the scaledobject has a cpu trigger but the container grafana doesn't have the cpu request defined`
	admissionDenied := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusForbidden,
		Reason:  metav1.StatusReasonForbidden,
		Message: admissionMessage,
	}}
	response = kubernetesResourceError(admissionDenied)
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT ||
		response.GetReason() != "AdmissionDenied" ||
		response.GetMessage() != admissionMessage {
		t.Fatalf("unexpected admission rejection response: %+v", response)
	}

	// An ordinary Forbidden is about cluster access rather than the submitted
	// object, and keeps a fixed message.
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "services"},
		"test1",
		errInternalDetail{},
	)
	response = kubernetesResourceError(forbidden)
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN ||
		response.GetMessage() != "Kubernetes API request failed" {
		t.Fatalf("unexpected forbidden response: %+v", response)
	}
	if kubernetesAdmissionDenied(forbidden) {
		t.Fatal("ordinary Kubernetes RBAC denial was classified as admission rejection")
	}

	// A rejection with no text of its own still says something.
	empty := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure,
		Code:   http.StatusUnprocessableEntity,
		Reason: metav1.StatusReasonInvalid,
	}}
	if message := kubernetesResourceError(empty).GetMessage(); message != "Kubernetes API request failed" {
		t.Fatalf("empty rejection message = %q", message)
	}
}

type errInternalDetail struct{}

func (errInternalDetail) Error() string { return "internal detail that must not travel" }
