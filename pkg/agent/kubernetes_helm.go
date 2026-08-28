package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	memorycache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// Helm runs here, in the Agent, and nowhere else.
//
// The Server has no Kubernetes connection and must not grow one, so the only
// side that can render a chart against a real API server and write the release
// is this one. Everything the operation needs travels with the request: the
// chart archive the Server fetched from its repository catalogue, the values
// document it assembled, and the switches an operator chose. Nothing is read
// from this Agent's filesystem — there is no Helm home here, no repository
// cache, no plugin directory — so a chart cannot be substituted by whatever
// happens to be on the node.
//
// Two rules are enforced on this side rather than only on the Server's:
//
//   - A rendered object may not name a Namespace other than the release's. The
//     Server authorized one Namespace; a chart that writes into a second one
//     would be spending an authorization nobody granted. Objects that name no
//     Namespace are unchanged and land in the release's, which is Helm's own
//     behaviour.
//   - Cluster-scoped objects are refused unless the request says the operator
//     was authorized for them. A CustomResourceDefinition or a ClusterRoleBinding
//     is not confined to a Namespace at all, so the Namespace grant that allowed
//     the install says nothing about it.
//
// Both are applied to what Helm actually rendered, after values and templates
// have had their say, because that is the only text that describes what would
// be written.

const (
	// Helm's own storage driver name for release Secrets. It is named
	// explicitly so a Helm default that changed later could not silently move
	// ZKE's releases into a storage the `helm` client would not find.
	helmStorageDriver = "secret"
	// What Helm calls a release it installed. Recorded on the revision so
	// `helm history` shows where the change came from.
	helmDescriptionPrefix = "ZKE: "
)

// helmHandlerConfig is what the Agent knows before any request arrives.
type helmHandlerConfig struct {
	// RESTConfig is a copy without the Agent's per-request timeout. A Helm
	// operation that waits for a Deployment to roll out holds one watch open
	// for the whole wait, and the Agent's ordinary two-minute request timeout
	// would cut it off mid-rollout and report a failure that did not happen.
	// Helm applies its own timeout instead, which the Server bounds.
	RESTConfig *rest.Config
	// MaxTimeout bounds what a request may ask to wait for, independently of
	// what it asks for. It is the Stream's timeout, so a request cannot outlive
	// the Stream carrying its answer.
	MaxTimeout time.Duration
}

// helmReplayResult and helmReplayCache reuse the Resource Stream's replay
// mechanism. A release change is the operation where a lost response costs the
// most: the caller cannot tell "the upgrade did not happen" from "it happened
// and the answer was lost", and retrying blind produces a second revision of an
// application that was already upgraded.
type helmReplayResult = mutationReplayResult[*agentv1.HelmResponse]

func newKubernetesHelmHandler(config helmHandlerConfig) agentprotocol.HelmHandler {
	replay := newMutationReplayCache[*agentv1.HelmResponse]()
	return func(
		ctx context.Context,
		request *agentv1.HelmRequest,
		valuesReader io.Reader,
		chartReader io.Reader,
		progress agentprotocol.HelmProgressSink,
	) (*agentv1.HelmResponse, io.Reader, error) {
		if config.RESTConfig == nil {
			return helmFailure(
				agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				http.StatusServiceUnavailable,
				"KubernetesClientUnavailable",
				"Kubernetes client is unavailable",
			), nil, nil
		}
		// Checked again here rather than trusting the Stream layer: this is the
		// last point before the Agent changes its own Cluster.
		if err := agentprotocol.ValidateHelmRequest(request); err != nil {
			return helmFailure(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"HelmRequestInvalid",
				"Helm request is invalid",
			), nil, nil
		}
		// The bodies are read here rather than inside the action so they can be
		// fingerprinted: a key reused for a *different* request has to be a
		// conflict rather than a replay of the first one's answer.
		values, failure := readHelmValues(valuesReader, request.GetValuesSize())
		if failure != nil {
			return failure, nil, nil
		}
		archive, failure := readHelmArchive(chartReader, request.GetChartSize())
		if failure != nil {
			return failure, nil, nil
		}
		loadedChart, failure := loadHelmChart(archive)
		if failure != nil {
			return failure, nil, nil
		}
		progress.Progress(helmOpeningLine(request, loadedChart))
		// Only the caller that actually runs Helm has anything to report. A
		// second dispatch of the same request collapses onto the first one's
		// result rather than running it twice, and inventing progress for it
		// would describe an execution this Stream is not performing.
		executed := false
		execute := func() (helmReplayResult, error) {
			executed = true
			report, failure := runHelmAction(ctx, config, request, values, loadedChart, progress)
			return helmOutcomeResult(report, failure, request.GetDryRun()), nil
		}
		var result helmReplayResult
		var conflict bool
		if key := agentprotocol.HelmIdempotencyKey(ctx); key != "" {
			fingerprint, err := mutationFingerprint(request, values.raw, archive)
			if err != nil {
				return helmFailure(
					agentv1.ResultCode_RESULT_CODE_INTERNAL,
					http.StatusInternalServerError,
					"HelmRequestUnhashable",
					"Helm request could not be fingerprinted",
				), nil, nil
			}
			result, conflict, err = replay.do(ctx, key, fingerprint, execute)
			if err != nil {
				return nil, nil, err
			}
		} else {
			result, _ = execute()
		}
		if conflict {
			return helmFailure(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"IdempotencyConflict",
				"idempotency key was already used for another Helm request",
			), nil, nil
		}
		if !executed {
			progress.Progress(
				"this request had already been performed; replaying its recorded outcome",
			)
		}
		if len(result.body) == 0 {
			return result.response, nil, nil
		}
		return result.response, bytes.NewReader(result.body), nil
	}
}

// helmNothingWritten are the refusals this Agent can prove changed nothing.
//
// They matter because of what reserving an idempotency key costs: the next
// attempt under that key is refused as a conflict, and after one of these the
// next attempt is normally the corrected one. Everything absent from this set is
// treated as "may have written" — once Helm has started applying, a failure can
// leave objects behind, and this Agent cannot tell that apart from one that did
// not.
//
// Each entry is a refusal made before a single object was applied: the request
// or its bodies were rejected, the Cluster could not be reached at all, Helm
// refused on its own storage before touching anything, or the manifest guard
// stopped the render — post-renderers run before the apply.
var helmNothingWritten = map[string]struct{}{
	"HelmRequestInvalid":            {},
	"KubernetesClientUnavailable":   {},
	"HelmValuesUnreadable":          {},
	"HelmValuesInvalid":             {},
	"HelmChartUnreadable":           {},
	"HelmChartDependenciesMissing":  {},
	"HelmConfigurationFailed":       {},
	"DiscoveryFailed":               {},
	"HelmActionUnsupported":         {},
	"HelmReleaseNoPreviousRevision": {},
	"HelmReleaseNotFound":           {},
	"HelmReleaseExists":             {},
	"HelmChartCrossNamespace":       {},
	"HelmChartClusterScoped":        {},
}

// helmOutcomeResult turns one action's outcome into a replayable record.
//
// `applied` decides whether the idempotency key stays reserved. A dry run wrote
// nothing, so it releases the key — which matters, because the Console previews
// and applies under the same key and the apply must not come back as a
// conflict. A refusal listed in helmNothingWritten releases it too. Everything
// else keeps it.
func helmOutcomeResult(
	report *helmrelease.Report,
	failure *agentv1.HelmResponse,
	dryRun bool,
) helmReplayResult {
	if failure != nil {
		_, harmless := helmNothingWritten[failure.GetReason()]
		return helmReplayResult{response: failure, applied: !dryRun && !harmless}
	}
	if report == nil {
		return helmReplayResult{
			response: &agentv1.HelmResponse{
				Result:               agentv1.ResultCode_RESULT_CODE_OK,
				KubernetesStatusCode: http.StatusOK,
			},
			applied: !dryRun,
		}
	}
	report.Truncate()
	body, err := json.Marshal(report)
	if err != nil {
		return helmReplayResult{
			response: helmFailure(
				agentv1.ResultCode_RESULT_CODE_INTERNAL,
				http.StatusInternalServerError,
				"HelmReportUnencodable",
				"Helm release report could not be encoded",
			),
			applied: !dryRun,
		}
	}
	if uint64(len(body)) > helmrelease.MaxReportBytes {
		return helmReplayResult{
			response: helmFailure(
				agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				http.StatusInsufficientStorage,
				"HelmReportTooLarge",
				"Helm release report exceeds the transferable size",
			),
			applied: !dryRun,
		}
	}
	return helmReplayResult{
		response: &agentv1.HelmResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
			BodySize:             uint64(len(body)),
		},
		body:    body,
		applied: !dryRun,
	}
}

// runHelmAction reads the request bodies and runs the one action they describe.
// It returns either a report or the response that explains why there is none;
// a Stream is never reset for a Kubernetes or a chart failure, which are
// ordinary answers to an ordinary question.
func runHelmAction(
	ctx context.Context,
	config helmHandlerConfig,
	request *agentv1.HelmRequest,
	values helmValues,
	loadedChart *chart.Chart,
	progress agentprotocol.HelmProgressSink,
) (*helmrelease.Report, *agentv1.HelmResponse) {
	configuration, guard, failure := newHelmConfiguration(config, request, progress)
	if failure != nil {
		return nil, failure
	}
	timeout := helmTimeout(config, request)
	// Helm's own wait is what `atomic` is built on: a rollback on failure needs
	// to know the operation failed, which needs the wait. Enabling it rather
	// than refusing the pair keeps the request meaning what it says.
	wait := request.GetWait() || request.GetAtomic()

	switch request.GetAction() {
	case agentv1.HelmAction_HELM_ACTION_INSTALL:
		install := action.NewInstall(configuration)
		install.ReleaseName = request.GetReleaseName()
		install.Namespace = request.GetNamespace()
		install.CreateNamespace = request.GetCreateNamespace()
		install.DryRun = request.GetDryRun()
		install.DisableHooks = request.GetDisableHooks()
		install.Wait = wait
		install.Atomic = request.GetAtomic()
		install.Timeout = timeout
		install.Description = helmDescription(request)
		install.PostRenderer = guard
		if request.GetDryRun() {
			// Without this Helm refuses to reach the API server at all and
			// renders against a guessed Kubernetes version, so capabilities a
			// chart branches on would be wrong in the very preview an operator
			// is being asked to approve.
			install.DryRunOption = "server"
		}
		released, err := install.RunWithContext(ctx, loadedChart, values.parsed)
		return helmOutcome(released, err, request, guard)
	case agentv1.HelmAction_HELM_ACTION_UPGRADE:
		upgrade := action.NewUpgrade(configuration)
		upgrade.Namespace = request.GetNamespace()
		upgrade.DryRun = request.GetDryRun()
		upgrade.DisableHooks = request.GetDisableHooks()
		upgrade.ResetValues = request.GetResetValues()
		upgrade.ReuseValues = request.GetReuseValues()
		upgrade.Wait = wait
		upgrade.Atomic = request.GetAtomic()
		upgrade.Timeout = timeout
		upgrade.MaxHistory = int(request.GetMaxHistory())
		upgrade.Description = helmDescription(request)
		upgrade.PostRenderer = guard
		if request.GetDryRun() {
			upgrade.DryRunOption = "server"
		}
		released, err := upgrade.RunWithContext(
			ctx,
			request.GetReleaseName(),
			loadedChart,
			values.parsed,
		)
		return helmOutcome(released, err, request, guard)
	case agentv1.HelmAction_HELM_ACTION_ROLLBACK:
		rollback := action.NewRollback(configuration)
		rollback.Version = int(request.GetRevision())
		rollback.DryRun = request.GetDryRun()
		rollback.DisableHooks = request.GetDisableHooks()
		rollback.Wait = wait
		rollback.Timeout = timeout
		rollback.MaxHistory = int(request.GetMaxHistory())
		// A rollback returns nothing, so what it produced has to be read back.
		// After a real one the newest stored revision *is* the result, which is
		// what version 0 reads.
		if err := rollback.Run(request.GetReleaseName()); err != nil {
			return nil, helmError(err)
		}
		version := 0
		if request.GetDryRun() {
			// A dry run wrote nothing, so reading the newest revision would
			// report what is already running — the opposite of a preview. The
			// report has to describe the revision that *would* be replayed, and
			// "the one before the current" has to be resolved to a number here
			// because only Helm's storage knows what the current one is.
			version = int(request.GetRevision())
			if version == 0 {
				current, err := action.NewGet(configuration).Run(request.GetReleaseName())
				if err != nil {
					return nil, helmError(err)
				}
				version = current.Version - 1
				if version < 1 {
					return nil, helmFailure(
						agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
						http.StatusNotFound,
						"HelmReleaseNoPreviousRevision",
						"this release has no revision before the current one to roll back to",
					)
				}
			}
		}
		get := action.NewGet(configuration)
		get.Version = version
		released, err := get.Run(request.GetReleaseName())
		return helmOutcome(released, err, request, guard)
	case agentv1.HelmAction_HELM_ACTION_UNINSTALL:
		uninstall := action.NewUninstall(configuration)
		uninstall.DryRun = request.GetDryRun()
		uninstall.DisableHooks = request.GetDisableHooks()
		uninstall.KeepHistory = request.GetKeepHistory()
		uninstall.Wait = wait
		uninstall.Timeout = timeout
		uninstall.Description = helmDescription(request)
		removed, err := uninstall.Run(request.GetReleaseName())
		if err != nil {
			return nil, helmError(err)
		}
		if removed == nil {
			return nil, helmFailure(
				agentv1.ResultCode_RESULT_CODE_INTERNAL,
				http.StatusInternalServerError,
				"HelmReleaseMissing",
				"Helm reported an uninstall without a release",
			)
		}
		report, failure := helmOutcome(removed.Release, nil, request, guard)
		if report != nil {
			report.Deleted = true
			// Helm's uninstall carries what it could not remove here. It is
			// prose, and it is the difference between "gone" and "gone except
			// for these", so it is kept rather than dropped.
			if removed.Info != "" {
				report.Description = strings.TrimSpace(
					report.Description + "\n" + removed.Info,
				)
			}
		}
		return report, failure
	default:
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmActionUnsupported",
			"Helm action is not supported",
		)
	}
}

// helmOpeningLine is the first thing the operator reads about an operation that
// has reached the Cluster.
//
// It says what this Agent is about to do, in the Cluster's own terms, so the
// log starts with the identity of the change rather than with whatever Helm
// happens to log first.
func helmOpeningLine(request *agentv1.HelmRequest, loadedChart *chart.Chart) string {
	verb := "running"
	switch request.GetAction() {
	case agentv1.HelmAction_HELM_ACTION_INSTALL:
		verb = "installing"
	case agentv1.HelmAction_HELM_ACTION_UPGRADE:
		verb = "upgrading"
	case agentv1.HelmAction_HELM_ACTION_ROLLBACK:
		verb = "rolling back"
	case agentv1.HelmAction_HELM_ACTION_UNINSTALL:
		verb = "uninstalling"
	}
	line := fmt.Sprintf(
		"%s %s in namespace %s",
		verb,
		request.GetReleaseName(),
		request.GetNamespace(),
	)
	if loadedChart != nil && loadedChart.Metadata != nil {
		line += fmt.Sprintf(
			" with chart %s-%s",
			loadedChart.Metadata.Name,
			loadedChart.Metadata.Version,
		)
	}
	if request.GetDryRun() {
		line += " (dry run: nothing will be written)"
	}
	return line
}

// helmValues keeps the parsed document together with the bytes it was parsed
// from: Helm needs the map, the replay fingerprint needs the exact text.
type helmValues struct {
	parsed map[string]any
	raw    []byte
}

func readHelmValues(reader io.Reader, size uint64) (helmValues, *agentv1.HelmResponse) {
	if size == 0 {
		return helmValues{parsed: map[string]any{}}, nil
	}
	raw, err := io.ReadAll(io.LimitReader(reader, int64(size)))
	if err != nil || uint64(len(raw)) != size {
		return helmValues{}, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmValuesUnreadable",
			"Helm values document could not be read from the request",
		)
	}
	parsed := map[string]any{}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return helmValues{}, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmValuesInvalid",
			"Helm values document is not a YAML mapping",
		)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return helmValues{parsed: parsed, raw: raw}, nil
}

// readHelmArchive reads the chart archive whole. It is held in memory rather
// than streamed into Helm's loader because the replay fingerprint has to cover
// it, and because the loader would otherwise consume the only copy.
func readHelmArchive(reader io.Reader, size uint64) ([]byte, *agentv1.HelmResponse) {
	if size == 0 {
		return nil, nil
	}
	archive, err := io.ReadAll(io.LimitReader(reader, int64(size)))
	if err != nil || uint64(len(archive)) != size {
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmChartUnreadable",
			"chart archive could not be read from the request",
		)
	}
	return archive, nil
}

func loadHelmChart(archive []byte) (*chart.Chart, *agentv1.HelmResponse) {
	if len(archive) == 0 {
		return nil, nil
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil {
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmChartUnreadable",
			"chart archive could not be loaded: "+helmMessage(err),
		)
	}
	if loaded == nil || loaded.Metadata == nil {
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmChartUnreadable",
			"chart archive carries no Chart.yaml",
		)
	}
	// A chart whose dependencies were never resolved renders without them and
	// installs an application missing half its parts. Helm's own client refuses
	// this; so does ZKE, rather than producing a release nobody asked for.
	if err := action.CheckDependencies(loaded, loaded.Metadata.Dependencies); err != nil {
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"HelmChartDependenciesMissing",
			"chart dependencies are not packaged with the chart: "+helmMessage(err),
		)
	}
	return loaded, nil
}

func newHelmConfiguration(
	config helmHandlerConfig,
	request *agentv1.HelmRequest,
	progress agentprotocol.HelmProgressSink,
) (*action.Configuration, *helmManifestGuard, *agentv1.HelmResponse) {
	getter := &helmRESTClientGetter{
		config:    config.RESTConfig,
		namespace: request.GetNamespace(),
	}
	configuration := &action.Configuration{}
	if err := configuration.Init(
		getter,
		request.GetNamespace(),
		helmStorageDriver,
		// Helm's own account of what it is doing, forwarded rather than
		// discarded. It is the only view anyone has into the minutes between
		// "applying" and "applied": Helm reports how many objects it is
		// creating, which ones it is waiting for, and why a wait ended. The
		// Server puts these lines in front of the operator as they arrive.
		func(format string, arguments ...any) {
			progress.Progress(fmt.Sprintf(format, arguments...))
		},
	); err != nil {
		return nil, nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			http.StatusServiceUnavailable,
			"HelmConfigurationFailed",
			"Helm could not be configured against this Cluster",
		)
	}
	mapper, err := getter.ToRESTMapper()
	if err != nil {
		return nil, nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			http.StatusServiceUnavailable,
			"DiscoveryFailed",
			"Kubernetes API discovery failed",
		)
	}
	return configuration, &helmManifestGuard{
		namespace:          request.GetNamespace(),
		mapper:             mapper,
		allowClusterScoped: request.GetAllowClusterScoped(),
	}, nil
}

// helmTimeout is what Helm is allowed to wait for, bounded by the Stream.
func helmTimeout(config helmHandlerConfig, request *agentv1.HelmRequest) time.Duration {
	maximum := config.MaxTimeout
	if maximum <= 0 {
		maximum = 10 * time.Minute
	}
	requested := time.Duration(request.GetTimeoutSeconds()) * time.Second
	if requested <= 0 || requested > maximum {
		return maximum
	}
	return requested
}

func helmDescription(request *agentv1.HelmRequest) string {
	description := strings.TrimSpace(request.GetDescription())
	if description == "" {
		return ""
	}
	return helmDescriptionPrefix + description
}

// helmOutcome turns what Helm returned into the report the Server publishes.
func helmOutcome(
	released *release.Release,
	err error,
	request *agentv1.HelmRequest,
	guard *helmManifestGuard,
) (*helmrelease.Report, *agentv1.HelmResponse) {
	if err != nil {
		// A guard refusal is the Agent's own decision and must not be reported
		// as a chart or Cluster failure: the operator has to be told which rule
		// they hit, not that "rendering failed".
		if refusal := guard.refusal(); refusal != nil {
			return nil, helmFailure(
				agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
				http.StatusForbidden,
				refusal.reason,
				refusal.message,
			)
		}
		return nil, helmError(err)
	}
	if released == nil {
		return nil, helmFailure(
			agentv1.ResultCode_RESULT_CODE_INTERNAL,
			http.StatusInternalServerError,
			"HelmReleaseMissing",
			"Helm reported success without a release",
		)
	}
	report := &helmrelease.Report{
		Name:          released.Name,
		Namespace:     released.Namespace,
		Revision:      int64(released.Version),
		DryRun:        request.GetDryRun(),
		Manifest:      released.Manifest,
		HooksDisabled: request.GetDisableHooks(),
	}
	if released.Info != nil {
		report.Status = released.Info.Status.String()
		report.Description = released.Info.Description
		report.Notes = released.Info.Notes
		if !released.Info.FirstDeployed.IsZero() {
			first := released.Info.FirstDeployed.Time.UTC()
			report.FirstDeployed = &first
		}
		if !released.Info.LastDeployed.IsZero() {
			last := released.Info.LastDeployed.Time.UTC()
			report.LastDeployed = &last
		}
	}
	if released.Chart != nil && released.Chart.Metadata != nil {
		report.ChartName = released.Chart.Metadata.Name
		report.ChartVersion = released.Chart.Metadata.Version
		report.AppVersion = released.Chart.Metadata.AppVersion
		report.ChartDescription = released.Chart.Metadata.Description
	}
	return report, nil
}

// helmError classifies what Helm failed with.
//
// The distinction that matters to an operator is the one between "the Cluster
// refused this", "there is no such release" and "the chart is wrong", because
// only the first is worth retrying and only the last is theirs to fix.
func helmError(err error) *agentv1.HelmResponse {
	switch {
	case errors.Is(err, driver.ErrReleaseNotFound),
		errors.Is(err, driver.ErrNoDeployedReleases):
		return helmFailure(
			agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			http.StatusNotFound,
			"HelmReleaseNotFound",
			helmMessage(err),
		)
	case errors.Is(err, driver.ErrReleaseExists):
		return helmFailure(
			agentv1.ResultCode_RESULT_CODE_CONFLICT,
			http.StatusConflict,
			"HelmReleaseExists",
			helmMessage(err),
		)
	case errors.Is(err, context.Canceled):
		return helmFailure(
			agentv1.ResultCode_RESULT_CODE_CANCELED,
			http.StatusRequestTimeout,
			"HelmCanceled",
			"Helm operation was canceled",
		)
	case errors.Is(err, context.DeadlineExceeded):
		return helmFailure(
			agentv1.ResultCode_RESULT_CODE_TIMEOUT,
			http.StatusGatewayTimeout,
			"HelmTimeout",
			"Helm operation did not finish within the allowed time",
		)
	}
	var statusError *apierrors.StatusError
	if errors.As(err, &statusError) {
		status := statusError.ErrStatus
		return helmFailure(
			helmResultForStatus(int(status.Code)),
			status.Code,
			string(status.Reason),
			helmMessage(err),
		)
	}
	return helmFailure(
		agentv1.ResultCode_RESULT_CODE_INTERNAL,
		http.StatusInternalServerError,
		"HelmOperationFailed",
		helmMessage(err),
	)
}

func helmResultForStatus(code int) agentv1.ResultCode {
	switch {
	case code == http.StatusUnauthorized:
		return agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED
	case code == http.StatusForbidden:
		return agentv1.ResultCode_RESULT_CODE_FORBIDDEN
	case code == http.StatusNotFound:
		return agentv1.ResultCode_RESULT_CODE_NOT_FOUND
	case code == http.StatusConflict:
		return agentv1.ResultCode_RESULT_CODE_CONFLICT
	case code == http.StatusRequestTimeout, code == http.StatusGatewayTimeout:
		return agentv1.ResultCode_RESULT_CODE_TIMEOUT
	case code >= 400 && code < 500:
		return agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT
	default:
		return agentv1.ResultCode_RESULT_CODE_INTERNAL
	}
}

// helmMessage bounds what an error is allowed to say. Helm concatenates the
// Kubernetes message for every object it failed on, so a failed install of a
// large chart produces an error far past what the protocol carries.
func helmMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "Helm operation failed"
	}
	const maximum = 2048
	if len(message) > maximum {
		return message[:maximum] + "…"
	}
	return message
}

func helmFailure(
	result agentv1.ResultCode,
	statusCode int32,
	reason string,
	message string,
) *agentv1.HelmResponse {
	if message == "" {
		message = reason
	}
	return &agentv1.HelmResponse{
		Result:               result,
		KubernetesStatusCode: statusCode,
		Reason:               reason,
		Message:              message,
	}
}

// helmManifestGuard inspects what a chart rendered before Helm applies it.
//
// It is a PostRenderer because that is the one point where the whole rendered
// text exists and nothing has been written yet. It changes nothing — the
// manifest is returned exactly as it arrived — and refuses the operation
// instead, which Helm surfaces as a rendering failure and the handler turns
// back into the refusal recorded here.
type helmManifestGuard struct {
	namespace          string
	mapper             meta.RESTMapper
	allowClusterScoped bool

	mutex    sync.Mutex
	refused  bool
	response helmRefusal
}

type helmRefusal struct {
	reason  string
	message string
}

type helmManifestObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

func (guard *helmManifestGuard) Run(rendered *bytes.Buffer) (*bytes.Buffer, error) {
	if guard == nil {
		return rendered, nil
	}
	for _, document := range releaseutil.SplitManifests(rendered.String()) {
		var object helmManifestObject
		if err := yaml.Unmarshal([]byte(document), &object); err != nil {
			// Not every document Helm renders is an object: a template may emit
			// a comment-only file. What cannot be parsed as an object carries no
			// Namespace to check, and Kubernetes will refuse it if it is not one.
			continue
		}
		if object.Kind == "" || object.APIVersion == "" {
			continue
		}
		if refusal := guard.check(object); refusal != nil {
			guard.refuse(*refusal)
			return nil, errors.New(refusal.message)
		}
	}
	return rendered, nil
}

func (guard *helmManifestGuard) check(object helmManifestObject) *helmRefusal {
	if object.Metadata.Namespace != "" &&
		object.Metadata.Namespace != guard.namespace {
		return &helmRefusal{
			reason: "HelmChartCrossNamespace",
			message: fmt.Sprintf(
				"chart renders %s/%s into Namespace %q, which is not the release Namespace %q",
				object.Kind,
				object.Metadata.Name,
				object.Metadata.Namespace,
				guard.namespace,
			),
		}
	}
	if guard.allowClusterScoped || guard.mapper == nil {
		return nil
	}
	groupVersion, err := schema.ParseGroupVersion(object.APIVersion)
	if err != nil {
		return nil
	}
	mapping, err := guard.mapper.RESTMapping(
		groupVersion.WithKind(object.Kind).GroupKind(),
		groupVersion.Version,
	)
	// A kind discovery does not know is a kind that does not exist in this
	// Cluster yet, which means it is defined by a CustomResourceDefinition in
	// this same chart — and that definition is itself a kind discovery does
	// know, so it is caught below and the whole install stops there. Letting
	// the unmappable document through therefore cannot be a way past this rule.
	if err != nil || mapping == nil {
		return nil
	}
	if mapping.Scope != nil && mapping.Scope.Name() == meta.RESTScopeNameRoot {
		return &helmRefusal{
			reason: "HelmChartClusterScoped",
			message: fmt.Sprintf(
				"chart renders cluster-scoped %s/%s, which needs authorization over the whole Cluster rather than over Namespace %q",
				object.Kind,
				object.Metadata.Name,
				guard.namespace,
			),
		}
	}
	return nil
}

func (guard *helmManifestGuard) refuse(refusal helmRefusal) {
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if guard.refused {
		return
	}
	guard.refused = true
	guard.response = refusal
}

func (guard *helmManifestGuard) refusal() *helmRefusal {
	if guard == nil {
		return nil
	}
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if !guard.refused {
		return nil
	}
	refusal := guard.response
	return &refusal
}

// helmRESTClientGetter hands Helm the connection this Agent already has.
//
// Helm's Kubernetes client is built for a CLI and expects a kubeconfig loader.
// This Agent has no kubeconfig — in a Cluster it has a ServiceAccount, and
// nothing else — so the loader it is given is an empty one carrying only the
// Namespace, which is the single thing Helm reads from it. Everything that
// actually reaches the API server comes from the rest.Config below.
type helmRESTClientGetter struct {
	config    *rest.Config
	namespace string

	mutex     sync.Mutex
	discovery discovery.CachedDiscoveryInterface
	mapper    meta.RESTMapper
}

var _ genericclioptions.RESTClientGetter = (*helmRESTClientGetter)(nil)

func (getter *helmRESTClientGetter) ToRESTConfig() (*rest.Config, error) {
	if getter.config == nil {
		return nil, errors.New("Kubernetes client configuration is unavailable")
	}
	return rest.CopyConfig(getter.config), nil
}

func (getter *helmRESTClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	getter.mutex.Lock()
	defer getter.mutex.Unlock()
	if getter.discovery != nil {
		return getter.discovery, nil
	}
	config, err := getter.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	// Discovery is bursty — Helm asks about every kind a chart renders — and
	// the defaults are the client-go throttle, which turns a large chart into a
	// minute of waiting. The cache below means each kind is asked about once.
	config.Burst = 100
	config.QPS = 50
	client, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	getter.discovery = memorycache.NewMemCacheClient(client)
	return getter.discovery, nil
}

func (getter *helmRESTClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	getter.mutex.Lock()
	mapper := getter.mapper
	getter.mutex.Unlock()
	if mapper != nil {
		return mapper, nil
	}
	client, err := getter.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	deferred := restmapper.NewDeferredDiscoveryRESTMapper(client)
	expander := restmapper.NewShortcutExpander(deferred, client, func(string) {})
	getter.mutex.Lock()
	getter.mapper = expander
	getter.mutex.Unlock()
	return expander, nil
}

func (getter *helmRESTClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(
		*clientcmdapi.NewConfig(),
		&clientcmd.ConfigOverrides{
			Context: clientcmdapi.Context{Namespace: getter.namespace},
		},
	)
}
