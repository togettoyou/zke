package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testTriggerKey = "0123456789abcdef"

func testCronJob(suspended bool) *batchv1.CronJob {
	return &batchv1.CronJob{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-report",
			Namespace: "analytics",
			UID:       "cronjob-uid",
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 2 * * *",
			Suspend:  &suspended,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "report"},
					Annotations: map[string]string{"owner": "analytics"},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{{
								Name:  "report",
								Image: "example/report:v3",
							}},
						},
					},
				},
			},
		},
	}
}

// Running a CronJob now must reach the Cluster as a Job create carrying the
// CronJob's own template, Kubernetes' manual-instantiate annotation and an owner
// reference that is not a controller reference. Anything else either runs the
// wrong thing or has the CronJob controller count this run against its own
// concurrency and history limits.
func TestTriggerCronJobCreatesAnOwnedManualJobFromTheTemplate(t *testing.T) {
	t.Parallel()

	var created map[string]any
	requester := &fakeResourceRequester{
		handle: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			responseBody io.Writer,
		) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET ||
				request.GetResource().GetResource() != "cronjobs" {
				t.Fatalf("unexpected read: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, testCronJob(false)), nil
		},
		mutate: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			body io.Reader,
			responseBody io.Writer,
			key string,
		) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
				request.GetResource().GetGroup() != "batch" ||
				request.GetResource().GetResource() != "jobs" ||
				request.GetNamespace() != "analytics" ||
				key != testTriggerKey {
				t.Fatalf("unexpected create: key=%q request=%+v", key, request)
			}
			if err := json.NewDecoder(body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			var job batchv1.Job
			raw, err := json.Marshal(created)
			if err != nil || json.Unmarshal(raw, &job) != nil {
				t.Fatalf("created object is not a Job: %v", err)
			}
			return writeKubernetesObject(t, responseBody, &job), nil
		},
	}
	result, err := NewService(requester).TriggerCronJob(context.Background(), TriggerCronJobInput{
		ClusterID:      testClusterID,
		Namespace:      "analytics",
		Name:           "nightly-report",
		UID:            "cronjob-uid",
		Confirm:        true,
		IdempotencyKey: testTriggerKey,
	})
	if err != nil {
		t.Fatalf("TriggerCronJob() err = %v", err)
	}
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if json.Unmarshal(raw, &job) != nil {
		t.Fatal("created Job did not decode")
	}
	if !strings.HasPrefix(job.Name, "nightly-report-manual-") ||
		job.Namespace != "analytics" ||
		job.Annotations[cronJobManualInstantiateAnnotation] != "manual" ||
		job.Annotations["owner"] != "analytics" ||
		job.Labels["app"] != "report" ||
		len(job.Spec.Template.Spec.Containers) != 1 ||
		job.Spec.Template.Spec.Containers[0].Image != "example/report:v3" {
		t.Fatalf("unexpected manual Job: %+v", job)
	}
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("manual Job owner references = %+v", job.OwnerReferences)
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "CronJob" || owner.Name != "nightly-report" ||
		string(owner.UID) != "cronjob-uid" ||
		owner.Controller == nil || *owner.Controller {
		t.Fatalf("manual Job is not owned-but-uncontrolled: %+v", owner)
	}
	if result.Resource != WorkloadJobs || result.Name != job.Name {
		t.Fatalf("TriggerCronJob() = %+v", result)
	}
}

// The same decision retried is the same idempotency key, and it has to ask for
// the same Job name so Kubernetes answers the second attempt with AlreadyExists
// rather than running the work a second time. Two different decisions must not
// collide.
func TestManualJobNameIsStableForOneDecisionAndDistinctBetweenThem(t *testing.T) {
	t.Parallel()

	first, err := manualJobName("nightly-report", testTriggerKey)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := manualJobName("nightly-report", testTriggerKey)
	if err != nil || repeat != first {
		t.Fatalf("manualJobName() not stable: %q vs %q (%v)", first, repeat, err)
	}
	other, err := manualJobName("nightly-report", testTriggerKey+"-second")
	if err != nil || other == first {
		t.Fatalf("manualJobName() collided across decisions: %q (%v)", other, err)
	}
	if _, err := manualJobName(strings.Repeat("a", 60), testTriggerKey); !errors.Is(err, ErrCronJobJobNameTooLong) {
		t.Fatalf("manualJobName() long name err = %v", err)
	}
}

// A suspended CronJob is paused on purpose, and a stale UID means the operator
// confirmed against a template that is no longer there. Neither may create a Job.
func TestTriggerCronJobRefusesSuspendedAndStaleTargets(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		suspended bool
		uid       string
		want      error
	}{
		"suspended": {suspended: true, uid: "cronjob-uid", want: ErrCronJobSuspended},
		"stale UID": {suspended: false, uid: "other-uid", want: ErrUpstreamConflict},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requester := &fakeResourceRequester{
				handle: func(
					_ context.Context,
					_ string,
					_ *agentv1.ResourceRequest,
					responseBody io.Writer,
				) (*agentv1.ResourceResponse, error) {
					return writeKubernetesObject(t, responseBody, testCronJob(testCase.suspended)), nil
				},
				mutate: func(
					context.Context,
					string,
					*agentv1.ResourceRequest,
					io.Reader,
					io.Writer,
					string,
				) (*agentv1.ResourceResponse, error) {
					t.Fatal("refused CronJob run reached the Cluster")
					return nil, nil
				},
			}
			_, err := NewService(requester).TriggerCronJob(context.Background(), TriggerCronJobInput{
				ClusterID:      testClusterID,
				Namespace:      "analytics",
				Name:           "nightly-report",
				UID:            testCase.uid,
				Confirm:        true,
				IdempotencyKey: testTriggerKey,
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("TriggerCronJob() err = %v, want %v", err, testCase.want)
			}
		})
	}
}

// The input guards, checked before anything is read from the Cluster.
func TestTriggerCronJobRefusesRequestsThatNameNoCronJobOrNoDecision(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("invalid CronJob run reached the Cluster")
			return nil, nil
		},
	}
	service := NewService(requester)
	valid := TriggerCronJobInput{
		ClusterID:      testClusterID,
		Namespace:      "analytics",
		Name:           "nightly-report",
		UID:            "cronjob-uid",
		Confirm:        true,
		IdempotencyKey: testTriggerKey,
	}
	for name, mutate := range map[string]func(*TriggerCronJobInput){
		"no namespace":    func(input *TriggerCronJobInput) { input.Namespace = "" },
		"no name":         func(input *TriggerCronJobInput) { input.Name = "" },
		"no UID":          func(input *TriggerCronJobInput) { input.UID = "" },
		"no confirmation": func(input *TriggerCronJobInput) { input.Confirm = false },
		"short key":       func(input *TriggerCronJobInput) { input.IdempotencyKey = "short" },
	} {
		input := valid
		mutate(&input)
		if _, err := service.TriggerCronJob(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: TriggerCronJob() err = %v, want ErrInvalidInput", name, err)
		}
	}
}
