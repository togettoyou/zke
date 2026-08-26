package kubernetesresource

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// Helm 3 keeps one Secret per release revision, and everything ZKE needs to
// list releases is already in that Secret's labels: `owner=helm` marks it,
// `name` is the release, `version` the revision, `status` its outcome. The
// release itself — chart, values, rendered manifest, notes — is the Secret's
// `release` value, Base64 over gzip over JSON.
const (
	helmReleaseSecretType    = "helm.sh/release.v1"
	helmReleaseSecretPrefix  = "sh.helm.release.v1."
	helmReleaseDataKey       = "release"
	helmOwnerLabel           = "owner"
	helmOwnerLabelValue      = "helm"
	helmReleaseNameLabel     = "name"
	helmReleaseVersionLabel  = "version"
	helmReleaseStatusLabel   = "status"
	helmReleaseOwnerSelector = helmOwnerLabel + "=" + helmOwnerLabelValue
)

const (
	// One page of release Secrets, read whole. Releases are grouped by name and
	// reduced to their newest revision, and a group split across pages would be
	// reduced to the wrong one — so this reads a bounded inventory and says so
	// when the Namespace holds more, rather than paging into a wrong answer.
	maximumHelmReleaseInventory int64 = 500
	// A release Secret is at most 1 MiB, and gzip over repetitive YAML expands
	// far past that. The decompressed release is bounded so a crafted or simply
	// enormous chart cannot turn one request into an unbounded allocation.
	maximumHelmReleaseBytes = 8 << 20
	// The rendered manifest is returned for reading, not for storage. A chart
	// that renders more than this is reported truncated; its objects remain
	// readable one by one through the resource browser.
	maximumHelmReleaseManifestBytes = 512 << 10
)

var (
	// ErrHelmReleaseInventoryTruncated is a Namespace holding more release
	// revisions than one inventory reads. Answering with a partial grouping
	// would name the wrong revision as current, which is worse than refusing.
	ErrHelmReleaseInventoryTruncated = errors.New("Helm release inventory exceeded the safety limit")
	// ErrHelmReleaseNotFound separates "no such release" from "no such Secret":
	// the caller asked about a release, and a Secret name is an implementation
	// detail of the storage driver.
	ErrHelmReleaseNotFound = errors.New("Helm release not found")
	// ErrHelmReleaseUnreadable is a release Secret this Server cannot decode —
	// a driver it does not know, a payload another tool wrote. It is not a
	// cluster failure and not a permission problem, and saying so keeps an
	// operator from looking for either.
	ErrHelmReleaseUnreadable = errors.New("Helm release payload cannot be decoded")
)

type ListHelmReleasesInput struct {
	ClusterID string
	Namespace string
}

// HelmRelease is one revision of one release, as the Secret's labels describe
// it. Nothing here requires decoding the payload.
type HelmRelease struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
	// SecretName is the object the revision is stored in. It is returned so an
	// operator can go and look at the storage itself — which, holding
	// `cluster.secret.read`, they may.
	SecretName string `json:"secret_name"`
	// Updated is when this revision's Secret was created, which is when Helm
	// wrote the revision. The release's own timestamps are in the detail.
	Updated time.Time `json:"updated"`
}

type HelmReleasePage struct {
	Releases []HelmRelease `json:"releases"`
}

// HelmReleaseDetail adds what only the payload knows.
type HelmReleaseDetail struct {
	HelmRelease
	Description      string     `json:"description"`
	ChartName        string     `json:"chart_name"`
	ChartVersion     string     `json:"chart_version"`
	AppVersion       string     `json:"app_version"`
	ChartDescription string     `json:"chart_description"`
	FirstDeployed    *time.Time `json:"first_deployed,omitempty"`
	LastDeployed     *time.Time `json:"last_deployed,omitempty"`
	// Notes is the chart's NOTES.txt as it was rendered for this release.
	Notes string `json:"notes"`
	// Values are the values the release was installed or upgraded with. A chart
	// is routinely handed a password this way, which is the whole reason these
	// endpoints require `cluster.secret.read`: this is the Secret's content,
	// reshaped, not a summary of it.
	Values map[string]any `json:"values"`
	// Manifest is what the chart rendered. Long manifests are cut, and the flag
	// says so rather than letting a reader believe they saw the end of it.
	Manifest          string `json:"manifest"`
	ManifestTruncated bool   `json:"manifest_truncated"`
}

// The subset of Helm's release record ZKE reads. The record also carries the
// whole chart — every template, every packaged file — and none of it is
// unmarshalled into anything this Server keeps.
type helmReleaseRecord struct {
	Name string `json:"name"`
	Info struct {
		FirstDeployed string `json:"first_deployed"`
		LastDeployed  string `json:"last_deployed"`
		Description   string `json:"description"`
		Status        string `json:"status"`
		Notes         string `json:"notes"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			AppVersion  string `json:"appVersion"`
			Description string `json:"description"`
		} `json:"metadata"`
	} `json:"chart"`
	Config    map[string]any `json:"config"`
	Manifest  string         `json:"manifest"`
	Version   int64          `json:"version"`
	Namespace string         `json:"namespace"`
}

// ListHelmReleases reports the Helm releases installed in one Namespace, each
// reduced to its newest revision — the same reduction `helm list` makes.
//
// Only the release Secrets' labels are read. Helm writes the release name, the
// revision and the status there, so a listing needs no decompression at all;
// what a listing would have to decompress is the chart and the values, and
// neither belongs in a page of many releases.
func (service *Service) ListHelmReleases(
	ctx context.Context,
	input ListHelmReleasesInput,
) (HelmReleasePage, error) {
	if len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return HelmReleasePage{}, ErrInvalidInput
	}
	page, err := service.ListSecrets(ctx, ListSecretsInput{
		ClusterID:     input.ClusterID,
		Namespace:     input.Namespace,
		Limit:         maximumHelmReleaseInventory,
		LabelSelector: helmReleaseOwnerSelector,
		FieldSelector: "type=" + helmReleaseSecretType,
	})
	if err != nil {
		return HelmReleasePage{}, err
	}
	if page.ContinueToken != "" {
		return HelmReleasePage{}, ErrHelmReleaseInventoryTruncated
	}
	newest := make(map[string]HelmRelease, len(page.Secrets))
	for _, secret := range page.Secrets {
		release, ok := helmReleaseFromSecret(secret)
		if !ok {
			continue
		}
		if current, exists := newest[release.Name]; exists && current.Revision >= release.Revision {
			continue
		}
		newest[release.Name] = release
	}
	releases := make([]HelmRelease, 0, len(newest))
	for _, release := range newest {
		releases = append(releases, release)
	}
	sort.Slice(releases, func(first, second int) bool {
		return releases[first].Name < releases[second].Name
	})
	return HelmReleasePage{Releases: releases}, nil
}

// ListHelmReleaseRevisions reports the stored history of one release, newest
// first. Helm keeps a Secret per revision, so the history is exactly what
// storage still holds: a release trimmed by `--history-max` has no record of
// the revisions that were dropped, and this does not pretend otherwise.
func (service *Service) ListHelmReleaseRevisions(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
) (HelmReleasePage, error) {
	if !validHelmReleaseName(namespace, name) {
		return HelmReleasePage{}, ErrInvalidInput
	}
	page, err := service.ListSecrets(ctx, ListSecretsInput{
		ClusterID: clusterID,
		Namespace: namespace,
		Limit:     maximumHelmReleaseInventory,
		LabelSelector: helmReleaseOwnerSelector + "," +
			helmReleaseNameLabel + "=" + name,
		FieldSelector: "type=" + helmReleaseSecretType,
	})
	if err != nil {
		return HelmReleasePage{}, err
	}
	if page.ContinueToken != "" {
		return HelmReleasePage{}, ErrHelmReleaseInventoryTruncated
	}
	releases := make([]HelmRelease, 0, len(page.Secrets))
	for _, secret := range page.Secrets {
		release, ok := helmReleaseFromSecret(secret)
		if !ok || release.Name != name {
			continue
		}
		releases = append(releases, release)
	}
	if len(releases) == 0 {
		return HelmReleasePage{}, ErrHelmReleaseNotFound
	}
	sort.Slice(releases, func(first, second int) bool {
		return releases[first].Revision > releases[second].Revision
	})
	return HelmReleasePage{Releases: releases}, nil
}

// GetHelmRelease reads one revision of one release. A revision of 0 asks for the
// newest one that storage still holds.
func (service *Service) GetHelmRelease(
	ctx context.Context,
	clusterID string,
	namespace string,
	name string,
	revision int64,
) (HelmReleaseDetail, error) {
	if !validHelmReleaseName(namespace, name) || revision < 0 {
		return HelmReleaseDetail{}, ErrInvalidInput
	}
	if revision == 0 {
		history, err := service.ListHelmReleaseRevisions(ctx, clusterID, namespace, name)
		if err != nil {
			return HelmReleaseDetail{}, err
		}
		revision = history.Releases[0].Revision
	}
	secretName := helmReleaseSecretName(name, revision)
	if len(k8svalidation.IsDNS1123Subdomain(secretName)) != 0 {
		return HelmReleaseDetail{}, ErrInvalidInput
	}
	secret, err := service.GetSecret(ctx, clusterID, namespace, secretName)
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return HelmReleaseDetail{}, ErrHelmReleaseNotFound
		}
		return HelmReleaseDetail{}, err
	}
	if secret.Type != helmReleaseSecretType {
		return HelmReleaseDetail{}, ErrHelmReleaseNotFound
	}
	record, err := decodeHelmRelease(secret.Data[helmReleaseDataKey])
	if err != nil {
		return HelmReleaseDetail{}, err
	}
	summary, ok := helmReleaseFromSecret(secret.SecretSummary)
	if !ok || summary.Name != name || summary.Revision != revision {
		return HelmReleaseDetail{}, ErrHelmReleaseUnreadable
	}
	detail := HelmReleaseDetail{
		HelmRelease:      summary,
		Description:      record.Info.Description,
		ChartName:        record.Chart.Metadata.Name,
		ChartVersion:     record.Chart.Metadata.Version,
		AppVersion:       record.Chart.Metadata.AppVersion,
		ChartDescription: record.Chart.Metadata.Description,
		FirstDeployed:    helmReleaseTime(record.Info.FirstDeployed),
		LastDeployed:     helmReleaseTime(record.Info.LastDeployed),
		Notes:            record.Info.Notes,
		Values:           record.Config,
		Manifest:         record.Manifest,
	}
	if detail.Values == nil {
		detail.Values = map[string]any{}
	}
	if len(detail.Manifest) > maximumHelmReleaseManifestBytes {
		detail.Manifest = detail.Manifest[:maximumHelmReleaseManifestBytes]
		detail.ManifestTruncated = true
	}
	// The labels are what the listing believed; the payload is what Helm wrote.
	// Where the payload is more specific it wins, and where it disagrees about
	// identity the Secret has been rewritten by something that is not Helm.
	if record.Name != "" && record.Name != name {
		return HelmReleaseDetail{}, ErrHelmReleaseUnreadable
	}
	if record.Info.Status != "" {
		detail.Status = record.Info.Status
	}
	return detail, nil
}

// helmReleaseFromSecret reads a release revision out of one Secret's labels, and
// reports whether the Secret is a release Secret at all. Anything with the
// release Secret's type but without Helm's labels is something else's object
// living in the same Namespace, and is left alone.
func helmReleaseFromSecret(secret SecretSummary) (HelmRelease, bool) {
	if secret.Type != helmReleaseSecretType ||
		secret.Labels[helmOwnerLabel] != helmOwnerLabelValue {
		return HelmRelease{}, false
	}
	name := secret.Labels[helmReleaseNameLabel]
	if name == "" || !strings.HasPrefix(secret.Name, helmReleaseSecretPrefix) {
		return HelmRelease{}, false
	}
	revision, err := strconv.ParseInt(secret.Labels[helmReleaseVersionLabel], 10, 64)
	if err != nil || revision <= 0 {
		return HelmRelease{}, false
	}
	return HelmRelease{
		Namespace:  secret.Namespace,
		Name:       name,
		Revision:   revision,
		Status:     secret.Labels[helmReleaseStatusLabel],
		SecretName: secret.Name,
		Updated:    secret.CreationTimestamp,
	}, true
}

// decodeHelmRelease unwraps the three layers Helm's Secret driver writes: the
// Kubernetes Base64 the Secret API returns, Helm's own Base64, and the gzip
// underneath it. Older Helm releases are not gzipped, which the magic number
// decides rather than a version check.
func decodeHelmRelease(value string) (helmReleaseRecord, error) {
	if value == "" {
		return helmReleaseRecord{}, ErrHelmReleaseUnreadable
	}
	stored, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return helmReleaseRecord{}, ErrInvalidResponse
	}
	payload, err := base64.StdEncoding.DecodeString(string(stored))
	if err != nil {
		return helmReleaseRecord{}, ErrHelmReleaseUnreadable
	}
	if len(payload) > 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return helmReleaseRecord{}, ErrHelmReleaseUnreadable
		}
		defer func() { _ = reader.Close() }()
		// One byte past the ceiling is read on purpose: it is the difference
		// between a release that just fits and one that does not.
		payload, err = io.ReadAll(io.LimitReader(reader, maximumHelmReleaseBytes+1))
		if err != nil {
			return helmReleaseRecord{}, ErrHelmReleaseUnreadable
		}
		if len(payload) > maximumHelmReleaseBytes {
			return helmReleaseRecord{}, fmt.Errorf(
				"%w: release exceeds %d bytes decompressed",
				ErrResponseTooLarge,
				maximumHelmReleaseBytes,
			)
		}
	}
	var record helmReleaseRecord
	if json.Unmarshal(payload, &record) != nil {
		return helmReleaseRecord{}, ErrHelmReleaseUnreadable
	}
	return record, nil
}

func helmReleaseSecretName(name string, revision int64) string {
	return helmReleaseSecretPrefix + name + ".v" + strconv.FormatInt(revision, 10)
}

func validHelmReleaseName(namespace string, name string) bool {
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0 &&
		name != "" && len(name) <= 253 &&
		len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}

func helmReleaseTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.IsZero() {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}
