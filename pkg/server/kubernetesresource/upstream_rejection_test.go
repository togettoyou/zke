package kubernetesresource

import (
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestResponseErrorKeepsKubernetesRejectionText(t *testing.T) {
	t.Parallel()

	detail := `Service "test1" is invalid: spec.ports[0].nodePort: Invalid value: 38080: ` +
		"provided port is not in the valid range. The range of valid ports is 30000-32767"
	err := responseErrorWithNotFound(&agentv1.ResourceResponse{
		Result:  agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
		Reason:  "Invalid",
		Message: detail,
	}, ErrResourceNotFound)
	if !errors.Is(err, ErrUpstreamRejected) {
		t.Fatalf("rejection error = %v, want ErrUpstreamRejected", err)
	}
	var rejection *UpstreamRejection
	if !errors.As(err, &rejection) || rejection.Detail() != detail {
		t.Fatalf("rejection detail = %v", err)
	}

	// A rejection this Server produced itself, without the API Server's account,
	// stays the generic invalid input it always was.
	err = responseErrorWithNotFound(&agentv1.ResourceResponse{
		Result: agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
		Reason: "BadRequest",
	}, ErrResourceNotFound)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("generic invalid input error = %v", err)
	}
}
