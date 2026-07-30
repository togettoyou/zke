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
			body: []byte(`{"ok":true}`),
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
