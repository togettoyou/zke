package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

var (
	// ErrReleaseRejected is the Cluster refusing the operation — a release that
	// already exists, an object Kubernetes would not accept, a rendered
	// manifest the Agent would not apply. It carries what the Agent said.
	ErrReleaseRejected = errors.New("target Cluster refused the Helm operation")
	// ErrUnsupported separates "this Agent is too old to run Helm" from "this
	// Agent is offline". Only one of them is fixed by waiting.
	ErrUnsupported = errors.New("target Cluster Agent does not support Helm release management")
	// ErrReportUnreadable is a successful operation whose report this Server
	// could not decode. The change happened; what is missing is the account of
	// it, and saying so is better than reporting a failure that did not occur.
	ErrReportUnreadable = errors.New("Helm release report could not be decoded")
)

// Stage names the part of a release change that is happening now.
//
// A release change is not one action but a short pipeline, and until it is
// finished the only honest answer to "what is it doing" is which part of that
// pipeline it is in. The Server resolves and fetches the chart, checks the
// values against the chart's own schema, and only then hands the whole thing to
// a Cluster that renders and applies it. Each of those can be the slow one — a
// chart downloaded from a public repository for the first time, a rollout
// waited on for minutes — and an operator who is told which is which knows
// whether to wait or to look for the problem.
//
// The stages are reported rather than inferred, because nothing about a
// pipeline's timing can be derived from its input.
type Stage string

const (
	// StageResolvingChart covers the repository index and the archive: which
	// version "latest" turned out to be, and getting the bytes.
	StageResolvingChart Stage = "resolving_chart"
	// StageValidatingValues is the chart's own values.schema.json, applied
	// before a Cluster is contacted.
	StageValidatingValues Stage = "validating_values"
	// StageExecuting is everything the Cluster does: rendering, applying, and
	// waiting for what was applied. Its messages are Helm's own log lines,
	// forwarded by the Agent as they happen.
	StageExecuting Stage = "executing"
)

// Progress reports one line of an operation that has not finished.
//
// A message may be empty, which says only that the operation has reached this
// stage. It is called from whichever goroutine is doing the work, including the
// one reading the Agent Stream, so an implementation must not block.
type Progress func(stage Stage, message string)

func (progress Progress) report(stage Stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

// ReleaseRejection carries the Agent's own words about a refusal, so a handler
// can pass the Cluster's reason to the operator rather than replacing it with
// a generic one.
type ReleaseRejection struct {
	Reason               string
	Message              string
	KubernetesStatusCode int32
}

func (rejection *ReleaseRejection) Error() string {
	if rejection.Message == "" {
		return ErrReleaseRejected.Error()
	}
	return rejection.Message
}

func (rejection *ReleaseRejection) Unwrap() error { return ErrReleaseRejected }

// InstallInput is a new release.
//
// Chart identity is a repository plus a name plus a version, never a URL: the
// operator chooses from the catalogue an administrator curated, and a request
// naming an arbitrary address would be a way to make this Server fetch from
// wherever the caller likes.
type InstallInput struct {
	ClusterID string
	Namespace string
	Name      string
	// RepositoryID and Chart identify what to install. Version may be empty,
	// which means the newest version the repository publishes — resolved once,
	// here, so the report says which version that turned out to be.
	RepositoryID string
	Chart        string
	Version      string
	// Values is the YAML document the operator edited. Empty means the chart's
	// own defaults.
	Values          string
	DryRun          bool
	CreateNamespace bool
	Wait            bool
	Atomic          bool
	DisableHooks    bool
	TimeoutSeconds  uint32
	MaxHistory      uint32
	Description     string
	// AllowClusterScoped is decided by the caller from the operator's
	// permissions, never sent by the Console. Without it the Agent refuses a
	// chart that renders an object no Namespace contains.
	AllowClusterScoped bool
	IdempotencyKey     string
	// Progress is optional and carries nothing the caller has to act on. It
	// exists because this operation can take minutes and the caller is showing
	// somebody a screen for all of them.
	Progress Progress
}

// UpgradeInput changes an existing release. It is an install's twin apart from
// the two switches that only mean anything when there is a previous revision.
type UpgradeInput struct {
	InstallInput
	ResetValues bool
	ReuseValues bool
}

type RollbackInput struct {
	ClusterID string
	Namespace string
	Name      string
	// Revision is the one to return to. Zero means the revision before the
	// current one, which is what `helm rollback` does with no argument.
	Revision           int64
	DryRun             bool
	Wait               bool
	DisableHooks       bool
	TimeoutSeconds     uint32
	MaxHistory         uint32
	Description        string
	AllowClusterScoped bool
	IdempotencyKey     string
	Progress           Progress
}

type UninstallInput struct {
	ClusterID string
	Namespace string
	Name      string
	// KeepHistory leaves the release's revisions in storage after its objects
	// are removed. It is what a later rollback needs, and it is why uninstall
	// is not simply a delete.
	KeepHistory        bool
	DryRun             bool
	Wait               bool
	DisableHooks       bool
	TimeoutSeconds     uint32
	Description        string
	AllowClusterScoped bool
	IdempotencyKey     string
	Progress           Progress
}

// Install renders and installs a chart into one Namespace of one Cluster.
func (service *Service) Install(
	ctx context.Context,
	input InstallInput,
) (helmrelease.Report, error) {
	return service.runWithChart(ctx, agentv1.HelmAction_HELM_ACTION_INSTALL, input, false, func(
		request *agentv1.HelmRequest,
	) {
		request.CreateNamespace = input.CreateNamespace
	})
}

// Upgrade replaces an installed release with a new revision.
func (service *Service) Upgrade(
	ctx context.Context,
	input UpgradeInput,
) (helmrelease.Report, error) {
	if input.ResetValues && input.ReuseValues {
		return helmrelease.Report{}, invalid(
			"reset_values and reuse_values cannot both be set",
		)
	}
	// ReuseValues merges the previous revision's values into these, and this
	// Server does not read release storage — so the document a schema would be
	// checked against is not the one in hand. Helm still validates on the Agent,
	// with the values it has; skipping here loses the earlier failure, not the
	// check.
	return service.runWithChart(
		ctx,
		agentv1.HelmAction_HELM_ACTION_UPGRADE,
		input.InstallInput,
		input.ReuseValues,
		func(request *agentv1.HelmRequest) {
			request.ResetValues = input.ResetValues
			request.ReuseValues = input.ReuseValues
		},
	)
}

// Rollback returns a release to a revision Helm still holds.
func (service *Service) Rollback(
	ctx context.Context,
	input RollbackInput,
) (helmrelease.Report, error) {
	request, err := baseHelmRequest(
		agentv1.HelmAction_HELM_ACTION_ROLLBACK,
		input.Namespace,
		input.Name,
		input.Description,
		input.TimeoutSeconds,
		input.AllowClusterScoped,
	)
	if err != nil {
		return helmrelease.Report{}, err
	}
	if input.Revision < 0 || input.MaxHistory > helmrelease.MaxHistoryLimit {
		return helmrelease.Report{}, ErrInvalidInput
	}
	request.Revision = input.Revision
	request.DryRun = input.DryRun
	request.Wait = input.Wait
	request.DisableHooks = input.DisableHooks
	request.MaxHistory = input.MaxHistory
	return service.send(
		ctx,
		input.ClusterID,
		request,
		nil,
		nil,
		input.IdempotencyKey,
		input.Progress,
	)
}

// Uninstall removes a release's objects, and optionally its history with them.
func (service *Service) Uninstall(
	ctx context.Context,
	input UninstallInput,
) (helmrelease.Report, error) {
	request, err := baseHelmRequest(
		agentv1.HelmAction_HELM_ACTION_UNINSTALL,
		input.Namespace,
		input.Name,
		input.Description,
		input.TimeoutSeconds,
		input.AllowClusterScoped,
	)
	if err != nil {
		return helmrelease.Report{}, err
	}
	request.KeepHistory = input.KeepHistory
	request.DryRun = input.DryRun
	request.Wait = input.Wait
	request.DisableHooks = input.DisableHooks
	return service.send(
		ctx,
		input.ClusterID,
		request,
		nil,
		nil,
		input.IdempotencyKey,
		input.Progress,
	)
}

// runWithChart is the shared body of install and upgrade: fetch the chart, hand
// it and the values to the Agent.
func (service *Service) runWithChart(
	ctx context.Context,
	action agentv1.HelmAction,
	input InstallInput,
	skipSchema bool,
	adjust func(*agentv1.HelmRequest),
) (helmrelease.Report, error) {
	request, err := baseHelmRequest(
		action,
		input.Namespace,
		input.Name,
		input.Description,
		input.TimeoutSeconds,
		input.AllowClusterScoped,
	)
	if err != nil {
		return helmrelease.Report{}, err
	}
	if input.MaxHistory > helmrelease.MaxHistoryLimit {
		return helmrelease.Report{}, ErrInvalidInput
	}
	values, err := normalizeValues(input.Values)
	if err != nil {
		return helmrelease.Report{}, err
	}
	// Fetching is also where the repository's signing policy is applied: an
	// archive that does not verify never becomes a request. See provenance.go.
	//
	// It is also, for a chart nobody on this Server has fetched before, the part
	// that takes the longest — a public index is megabytes and the archive
	// behind it is a download. Saying so before it starts is the difference
	// between a slow step and a frozen page.
	input.Progress.report(StageResolvingChart, chartRequestLine(input))
	fetched, err := service.fetchChartArchive(
		ctx,
		input.RepositoryID,
		input.Chart,
		input.Version,
	)
	if err != nil {
		return helmrelease.Report{}, err
	}
	archive := fetched.Archive
	if uint64(len(archive)) > helmrelease.MaxChartBytes {
		return helmrelease.Report{}, ErrChartTooLarge
	}
	input.Progress.report(
		StageResolvingChart,
		fetchedChartLine(input.Chart, fetched, len(archive)),
	)
	input.Progress.report(StageValidatingValues, "")
	if err := service.validateValues(archive, values, skipSchema); err != nil {
		return helmrelease.Report{}, err
	}
	request.DryRun = input.DryRun
	request.Wait = input.Wait
	request.Atomic = input.Atomic
	request.DisableHooks = input.DisableHooks
	request.MaxHistory = input.MaxHistory
	request.ValuesSize = uint64(len(values))
	request.ChartSize = uint64(len(archive))
	adjust(request)
	return service.send(
		ctx,
		input.ClusterID,
		request,
		values,
		archive,
		input.IdempotencyKey,
		input.Progress,
	)
}

// chartRequestLine names what is about to be fetched. An empty version is
// reported as "latest" rather than as nothing, because that is what it means.
func chartRequestLine(input InstallInput) string {
	version := strings.TrimSpace(input.Version)
	if version == "" {
		version = "latest"
	}
	return fmt.Sprintf("resolving chart %s@%s", input.Chart, version)
}

// fetchedChartLine reports what the fetch turned out to be. The resolved
// version is the interesting half: "latest" is a question, and this is the
// answer the whole operation will be recorded against.
func fetchedChartLine(name string, fetched fetchedChart, size int) string {
	version := strings.TrimSpace(fetched.Version)
	if version == "" {
		version = "unknown version"
	}
	line := fmt.Sprintf("fetched chart %s@%s (%d KiB)", name, version, (size+1023)/1024)
	switch {
	case fetched.Signature.Verified:
		line += "; provenance verified"
	case fetched.Signature.Unsigned:
		line += "; published without a provenance file"
	}
	return line
}

// send performs one Agent round trip and decodes the report.
func (service *Service) send(
	ctx context.Context,
	clusterID string,
	request *agentv1.HelmRequest,
	values []byte,
	archive []byte,
	idempotencyKey string,
	progress Progress,
) (helmrelease.Report, error) {
	if !isUUID(clusterID) {
		return helmrelease.Report{}, ErrInvalidInput
	}
	// Checked here as well as by the Stream layer, so an input this service
	// assembled wrongly is caught before it reaches a Cluster.
	if err := agentprotocol.ValidateHelmRequest(request); err != nil {
		return helmrelease.Report{}, invalid("%s", err)
	}
	report := &bytes.Buffer{}
	progress.report(StageExecuting, "")
	var forward func(*agentv1.HelmProgress)
	if progress != nil {
		forward = func(line *agentv1.HelmProgress) {
			progress(StageExecuting, line.GetMessage())
		}
	}
	response, err := service.agents.RequestHelm(
		ctx,
		clusterID,
		request,
		bytes.NewReader(values),
		bytes.NewReader(archive),
		report,
		idempotencyKey,
		forward,
	)
	if err != nil {
		return helmrelease.Report{}, err
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		return helmrelease.Report{}, &ReleaseRejection{
			Reason:               response.GetReason(),
			Message:              response.GetMessage(),
			KubernetesStatusCode: response.GetKubernetesStatusCode(),
		}
	}
	if report.Len() == 0 {
		// A successful operation with nothing to report: an uninstall that kept
		// no history leaves no revision behind. Reporting the request's own
		// identity is more useful than reporting nothing.
		return helmrelease.Report{
			Name:      request.GetReleaseName(),
			Namespace: request.GetNamespace(),
			DryRun:    request.GetDryRun(),
			Deleted:   request.GetAction() == agentv1.HelmAction_HELM_ACTION_UNINSTALL,
		}, nil
	}
	var decoded helmrelease.Report
	if err := json.Unmarshal(report.Bytes(), &decoded); err != nil {
		return helmrelease.Report{}, ErrReportUnreadable
	}
	return decoded, nil
}

func baseHelmRequest(
	action agentv1.HelmAction,
	namespace string,
	name string,
	description string,
	timeoutSeconds uint32,
	allowClusterScoped bool,
) (*agentv1.HelmRequest, error) {
	if len(k8svalidation.IsDNS1123Label(namespace)) != 0 {
		return nil, invalid("Namespace is not a valid Kubernetes name")
	}
	if !validReleaseName(name) {
		return nil, invalid(
			"release name must be at most 53 lowercase alphanumeric characters, '-' or '.'",
		)
	}
	description = strings.TrimSpace(description)
	if len(description) > helmrelease.MaxDescriptionLength {
		description = description[:helmrelease.MaxDescriptionLength]
	}
	if timeoutSeconds > helmrelease.MaxTimeoutSeconds {
		return nil, invalid("timeout is out of range")
	}
	return &agentv1.HelmRequest{
		Action:             action,
		Namespace:          namespace,
		ReleaseName:        name,
		Description:        description,
		TimeoutSeconds:     timeoutSeconds,
		AllowClusterScoped: allowClusterScoped,
	}, nil
}

// validReleaseName is Helm's own rule, applied before a Cluster is contacted so
// the failure names the field rather than arriving as a rendering error.
func validReleaseName(name string) bool {
	if name == "" || len(name) > 53 {
		return false
	}
	return len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}

// normalizeValues checks that what the operator wrote is a YAML mapping before
// it is sent anywhere. A values document that is a list, or a string, renders
// into something no chart expects, and finding that out from a template error
// is worse than being told here.
func normalizeValues(document string) ([]byte, error) {
	trimmed := strings.TrimSpace(document)
	if trimmed == "" {
		return nil, nil
	}
	if uint64(len(trimmed)) > helmrelease.MaxValuesBytes {
		return nil, invalid(
			"values document exceeds %d bytes",
			helmrelease.MaxValuesBytes,
		)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, invalid("values document is not a YAML mapping")
	}
	return []byte(trimmed), nil
}
