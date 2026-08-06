package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestResourceMutationCacheReplaysAndRejectsKeyReuse(t *testing.T) {
	t.Parallel()

	cache := newResourceMutationCache()
	request := &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource: &agentv1.GroupVersionResource{
			Version:  "v1",
			Resource: "configmaps",
		},
		BodySize: 2,
	}
	fingerprint, err := mutationFingerprint(request, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int64
	execute := func() (resourceMutationResult, error) {
		executions.Add(1)
		return resourceMutationResult{
			response: &agentv1.ResourceResponse{
				Result: agentv1.ResultCode_RESULT_CODE_OK,
			},
			body:    []byte(`{"ok":true}`),
			applied: true,
		}, nil
	}
	first, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		fingerprint,
		execute,
	)
	if err != nil || conflict || first.response == nil {
		t.Fatalf("first mutation result=%+v conflict=%v err=%v", first, conflict, err)
	}
	second, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		fingerprint,
		execute,
	)
	if err != nil || conflict || string(second.body) != `{"ok":true}` ||
		executions.Load() != 1 {
		t.Fatalf(
			"replay result=%+v conflict=%v executions=%d err=%v",
			second,
			conflict,
			executions.Load(),
			err,
		)
	}
	otherFingerprint, err := mutationFingerprint(request, []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	_, conflict, err = cache.do(
		context.Background(),
		"0123456789abcdef",
		otherFingerprint,
		func() (resourceMutationResult, error) {
			return resourceMutationResult{}, errors.New("must not execute")
		},
	)
	if err != nil || !conflict || executions.Load() != 1 {
		t.Fatalf(
			"key reuse conflict=%v executions=%d err=%v",
			conflict,
			executions.Load(),
			err,
		)
	}
}

// A mutation Kubernetes refused never happened, so its key stays free: the next
// attempt is the operator's corrected request, and rejecting that as a duplicate
// would leave them nothing to correct — the form would have to be abandoned and
// filled in again.
func TestResourceMutationCacheReleasesKeyForRefusedMutation(t *testing.T) {
	t.Parallel()

	cache := newResourceMutationCache()
	request := &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource: &agentv1.GroupVersionResource{
			Version:  "v1",
			Resource: "configmaps",
		},
		BodySize: 2,
	}
	refusedFingerprint, err := mutationFingerprint(request, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	refused, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		refusedFingerprint,
		func() (resourceMutationResult, error) {
			return resourceMutationResult{
				response: &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_CONFLICT,
					KubernetesStatusCode: 409,
					Reason:               "AlreadyExists",
				},
			}, nil
		},
	)
	if err != nil || conflict || refused.response.GetReason() != "AlreadyExists" {
		t.Fatalf("refused result=%+v conflict=%v err=%v", refused, conflict, err)
	}

	correctedFingerprint, err := mutationFingerprint(request, []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	var executed atomic.Int64
	corrected, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		correctedFingerprint,
		func() (resourceMutationResult, error) {
			executed.Add(1)
			return resourceMutationResult{
				response: &agentv1.ResourceResponse{
					Result: agentv1.ResultCode_RESULT_CODE_OK,
				},
				applied: true,
			}, nil
		},
	)
	if err != nil || conflict || executed.Load() != 1 ||
		corrected.response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf(
			"corrected result=%+v conflict=%v executions=%d err=%v",
			corrected,
			conflict,
			executed.Load(),
			err,
		)
	}

	// The corrected attempt did apply, so from here the key protects it again.
	_, conflict, err = cache.do(
		context.Background(),
		"0123456789abcdef",
		refusedFingerprint,
		func() (resourceMutationResult, error) {
			return resourceMutationResult{}, errors.New("must not execute")
		},
	)
	if err != nil || !conflict || executed.Load() != 1 {
		t.Fatalf(
			"reuse after applied mutation conflict=%v executions=%d err=%v",
			conflict,
			executed.Load(),
			err,
		)
	}
}

// A failure the Agent cannot account for keeps its key: the write may have
// reached etcd before the connection did not come back, which is the whole
// reason the replay cache exists.
func TestResourceMutationCacheKeepsKeyForUnaccountedFailure(t *testing.T) {
	t.Parallel()

	cache := newResourceMutationCache()
	request := &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource: &agentv1.GroupVersionResource{
			Version:  "v1",
			Resource: "configmaps",
		},
		BodySize: 2,
	}
	fingerprint, err := mutationFingerprint(request, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		fingerprint,
		func() (resourceMutationResult, error) {
			return resourceMutationResult{
				response: &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_TIMEOUT,
					KubernetesStatusCode: 504,
					Reason:               "Timeout",
				},
				applied: mutationFailureApplied(504),
			}, nil
		},
	); err != nil || conflict {
		t.Fatalf("timeout conflict=%v err=%v", conflict, err)
	}

	other, err := mutationFingerprint(request, []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	_, conflict, err := cache.do(
		context.Background(),
		"0123456789abcdef",
		other,
		func() (resourceMutationResult, error) {
			return resourceMutationResult{}, errors.New("must not execute")
		},
	)
	if err != nil || !conflict {
		t.Fatalf("key reuse after timeout conflict=%v err=%v", conflict, err)
	}
}

func TestMutationFailureApplied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int32
		applied bool
	}{
		{name: "canceled without status", status: 0, applied: true},
		{name: "invalid object", status: 422, applied: false},
		{name: "already exists", status: 409, applied: false},
		{name: "forbidden", status: 403, applied: false},
		{name: "too many requests", status: 429, applied: false},
		{name: "server error", status: 500, applied: true},
		{name: "gateway timeout", status: 504, applied: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationFailureApplied(test.status); got != test.applied {
				t.Fatalf(
					"mutationFailureApplied(%d) = %t, want %t",
					test.status,
					got,
					test.applied,
				)
			}
		})
	}
}
