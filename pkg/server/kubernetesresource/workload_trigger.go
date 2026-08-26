package kubernetesresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// The annotation Kubernetes itself uses to mark a Job that a person asked for
// rather than one the schedule produced. Writing the same key `kubectl create
// job --from=cronjob/...` writes keeps a manually run Job recognisable to
// everything that already knows how to read it, this Console included.
const cronJobManualInstantiateAnnotation = "cronjob.kubernetes.io/instantiate"

// The suffix that separates one manual run from the next. Kubernetes allows a
// Job name of 63 characters, and the generated suffix has to fit inside that
// budget together with the CronJob's own name.
const (
	cronJobManualNameInfix  = "-manual-"
	cronJobManualNameDigits = 8
	maximumJobNameLength    = 63
)

// ErrCronJobSuspended is a run-now request against a CronJob that is paused.
//
// Kubernetes would accept the Job — `spec.suspend` stops the schedule, not the
// API — and accepting it is still the wrong answer. Someone paused this CronJob
// on purpose, and a Console that runs it anyway turns "paused" into a label
// rather than a state. Resuming it first is one click and makes the decision
// visible.
var ErrCronJobSuspended = errors.New("Kubernetes CronJob is suspended")

// ErrCronJobJobNameTooLong is a CronJob whose name leaves no room for the run
// suffix. Kubernetes would refuse the Job with a validation error naming a
// length nobody asked for; saying it here names the CronJob instead.
var ErrCronJobJobNameTooLong = errors.New("Kubernetes CronJob name leaves no room for a manual Job name")

type TriggerCronJobInput struct {
	ClusterID string
	Namespace string
	Name      string
	// UID pins the trigger to the CronJob the operator was looking at. A name is
	// reusable — a CronJob deleted and recreated with a different schedule and a
	// different image keeps it — so without this a confirmation could run a
	// template nobody reviewed.
	UID            string
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

// TriggerCronJob creates one Job from a CronJob's template, now, without
// touching the schedule.
//
// This is the console equivalent of `kubectl create job --from=cronjob/x`, and
// it is a create rather than a patch: the CronJob is read, never written, and
// what reaches the Cluster is a new Job object. That is why it answers to
// `cluster.resource.create` and not to the update permission — an operator who
// may create workloads in a Namespace may run one of its CronJobs now, and one
// who may only edit existing objects may not.
//
// The Job carries an owner reference back to the CronJob, as kubectl's does.
// Without it a manually run Job outlives the CronJob it came from and is left
// behind when the CronJob is deleted; with it, Kubernetes garbage collection
// treats it like any other Job the CronJob created.
func (service *Service) TriggerCronJob(
	ctx context.Context,
	input TriggerCronJobInput,
) (WorkloadDetail, error) {
	if len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.Name)) != 0 ||
		strings.TrimSpace(input.UID) == "" || len(input.UID) > 128 ||
		(!input.DryRun && !input.Confirm) ||
		!validIdempotencyKey(input.IdempotencyKey) {
		return WorkloadDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID,
		Resource:  workloadIdentities[WorkloadCronJobs],
		Namespace: input.Namespace,
		Name:      input.Name,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	var cronJob batchv1.CronJob
	if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &cronJob) != nil ||
		cronJob.Name != input.Name || cronJob.Namespace != input.Namespace {
		return WorkloadDetail{}, ErrInvalidResponse
	}
	if string(cronJob.UID) != input.UID {
		return WorkloadDetail{}, ErrUpstreamConflict
	}
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		return WorkloadDetail{}, ErrCronJobSuspended
	}
	job, err := manualJobFromCronJob(&cronJob, input.IdempotencyKey)
	if err != nil {
		return WorkloadDetail{}, err
	}
	created, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID:      input.ClusterID,
		Resource:       workloadIdentities[WorkloadJobs],
		Namespace:      input.Namespace,
		Object:         job,
		Options:        MutationOptions{DryRun: input.DryRun},
		Confirm:        input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return WorkloadDetail{}, err
	}
	return workloadDetail(created, WorkloadJobs, input.Namespace, "")
}

// manualJobFromCronJob stamps the CronJob's template into a Job object.
//
// The name is derived from the idempotency key rather than from a clock or a
// random source, so a retried request — the same key, because it is the same
// decision — asks for the same Job name. Kubernetes then answers the second
// attempt with AlreadyExists instead of running the work twice, which is the
// property a run-now button needs from a network that may drop a response.
func manualJobFromCronJob(
	cronJob *batchv1.CronJob,
	idempotencyKey string,
) (map[string]any, error) {
	name, err := manualJobName(cronJob.Name, idempotencyKey)
	if err != nil {
		return nil, err
	}
	template := cronJob.Spec.JobTemplate
	labels := make(map[string]string, len(template.Labels))
	for key, value := range template.Labels {
		labels[key] = value
	}
	annotations := make(map[string]string, len(template.Annotations)+1)
	for key, value := range template.Annotations {
		annotations[key] = value
	}
	annotations[cronJobManualInstantiateAnnotation] = "manual"
	// Owned but not controlled: the CronJob controller adopts the Jobs it
	// created itself, and claiming to be one of them would have this run count
	// against `concurrencyPolicy` and against the history limits. Garbage
	// collection follows the reference either way, which is the part this needs.
	owned := false
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cronJob.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "batch/v1",
				Kind:               "CronJob",
				Name:               cronJob.Name,
				UID:                cronJob.UID,
				Controller:         &owned,
				BlockOwnerDeletion: &owned,
			}},
		},
		Spec: *template.Spec.DeepCopy(),
	}
	converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return converted, nil
}

func manualJobName(cronJobName string, idempotencyKey string) (string, error) {
	digest := sha256.Sum256([]byte(cronJobName + "\x00" + idempotencyKey))
	suffix := cronJobManualNameInfix + hex.EncodeToString(digest[:])[:cronJobManualNameDigits]
	if len(cronJobName)+len(suffix) > maximumJobNameLength {
		return "", ErrCronJobJobNameTooLong
	}
	return cronJobName + suffix, nil
}
