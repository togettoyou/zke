package platformsettings

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput    = errors.New("invalid platform setting")
	ErrNotFound        = errors.New("Agent endpoint profile not found")
	ErrConflict        = errors.New("platform setting conflict")
	ErrInUse           = errors.New("Agent endpoint profile is in use")
	ErrEndpointUnready = errors.New("Agent endpoint profile is not ready")
)

type invalidInputError struct {
	detail string
}

func (err invalidInputError) Error() string  { return err.detail }
func (err invalidInputError) Unwrap() error  { return ErrInvalidInput }
func (err invalidInputError) Detail() string { return err.detail }

func invalidInput(detail string) error {
	return invalidInputError{detail: detail}
}

const (
	ProfileStatusReady       = "ready"
	ProfileStatusDisabled    = "disabled"
	ProfileStatusUnavailable = "unavailable"
)

type ListenerCertificateReconciler func(
	context.Context,
	[]string,
	[]string,
	time.Time,
) error

type Store interface {
	ListEndpointProfiles(context.Context) ([]store.AgentEndpointProfile, error)
	GetEndpointProfile(context.Context, string) (store.AgentEndpointProfile, error)
	CreateEndpointProfile(context.Context, store.CreateAgentEndpointProfileParams) (store.AgentEndpointProfile, error)
	UpdateEndpointProfile(context.Context, store.UpdateAgentEndpointProfileParams) (store.AgentEndpointProfile, error)
	DeleteEndpointProfile(context.Context, string) error
	GetSettings(context.Context) (store.PlatformSettings, error)
	HasEnrollments(context.Context) (bool, error)
	UpdateSettings(context.Context, store.UpdatePlatformSettingsParams) (store.PlatformSettings, error)
}

type Service struct {
	store                   Store
	listenerCertificateFile string
	reconcileListener       ListenerCertificateReconciler
	profileMutation         sync.Mutex
}

type Profile struct {
	ID                           string
	Name                         string
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	Enabled                      bool
	Status                       string
	Revision                     int64
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type Settings struct {
	DefaultEndpointProfileID string
	AgentImage               string
	AgentNamespace           string
	AgentImagePullPolicy     string
	ClusterTerminalImage     string
	Revision                 int64
	UpdatedAt                time.Time
}

type ProfileInput struct {
	ID                           string
	Name                         string
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	Enabled                      bool
	ExpectedRevision             int64
	ActorUserID                  string
	Now                          time.Time
}

type SettingsInput struct {
	AgentImage           string
	AgentNamespace       string
	AgentImagePullPolicy string
	ClusterTerminalImage string
	ExpectedRevision     int64
	ActorUserID          string
	Now                  time.Time
}

type Snapshot = store.EnrollmentConfigurationSnapshot

func NewService(
	settingsStore Store,
	listenerCertificateFile string,
	reconcileListener ListenerCertificateReconciler,
) *Service {
	return &Service{
		store:                   settingsStore,
		listenerCertificateFile: listenerCertificateFile,
		reconcileListener:       reconcileListener,
	}
}

func (service *Service) Get(ctx context.Context) (Settings, []Profile, error) {
	settings, err := service.store.GetSettings(ctx)
	if err != nil {
		return Settings{}, nil, err
	}
	profiles, err := service.store.ListEndpointProfiles(ctx)
	if err != nil {
		return Settings{}, nil, err
	}
	return settingsFromStore(settings), defaultProfileFirst(
		service.profilesFromStore(profiles),
		settings.DefaultEndpointProfileID,
	), nil
}

func (service *Service) ListReadyProfiles(ctx context.Context) (string, []Profile, error) {
	settings, err := service.store.GetSettings(ctx)
	if err != nil {
		return "", nil, err
	}
	profiles, err := service.store.ListEndpointProfiles(ctx)
	if err != nil {
		return "", nil, err
	}
	result := make([]Profile, 0, len(profiles))
	for _, profile := range service.profilesFromStore(profiles) {
		if profile.Status == ProfileStatusReady {
			result = append(result, profile)
		}
	}
	return settings.DefaultEndpointProfileID, defaultProfileFirst(
		result,
		settings.DefaultEndpointProfileID,
	), nil
}

func defaultProfileFirst(profiles []Profile, defaultProfileID string) []Profile {
	for index := range profiles {
		if profiles[index].ID != defaultProfileID || index == 0 {
			continue
		}
		result := append([]Profile(nil), profiles...)
		defaultProfile := result[index]
		copy(result[1:index+1], result[:index])
		result[0] = defaultProfile
		return result
	}
	return profiles
}

func (service *Service) CreateProfile(ctx context.Context, input ProfileInput) (Profile, error) {
	if err := validateProfileInput(input, false); err != nil {
		return Profile{}, err
	}
	service.profileMutation.Lock()
	defer service.profileMutation.Unlock()
	id, err := identifier.NewUUID()
	if err != nil {
		return Profile{}, err
	}
	originalProfiles, err := service.reconcileCandidate(ctx, store.AgentEndpointProfile{
		ID: id, Name: strings.TrimSpace(input.Name), RegistrationURL: strings.TrimSpace(input.RegistrationURL),
		QUICAddress: strings.TrimSpace(input.QUICAddress), Enabled: input.Enabled,
	}, input.Now)
	if err != nil {
		return Profile{}, err
	}
	created, err := service.store.CreateEndpointProfile(ctx, store.CreateAgentEndpointProfileParams{
		ID: id, Name: strings.TrimSpace(input.Name), RegistrationURL: strings.TrimSpace(input.RegistrationURL),
		QUICAddress:                  strings.TrimSpace(input.QUICAddress),
		RegistrationCACertificatePEM: strings.TrimSpace(input.RegistrationCACertificatePEM),
		Enabled:                      input.Enabled, ActorUserID: input.ActorUserID, Now: input.Now,
	})
	if err != nil {
		restoreErr := service.reconcileProfiles(ctx, originalProfiles, input.Now)
		if restoreErr != nil {
			return Profile{}, errors.Join(err, fmt.Errorf("restore Agent Listener certificate: %w", restoreErr))
		}
	}
	if errors.Is(err, store.ErrEndpointProfileConflict) {
		return Profile{}, ErrConflict
	}
	if err != nil {
		return Profile{}, err
	}
	return service.profileFromStore(created), nil
}

func (service *Service) UpdateProfile(ctx context.Context, input ProfileInput) (Profile, error) {
	if err := validateProfileInput(input, true); err != nil {
		return Profile{}, err
	}
	service.profileMutation.Lock()
	defer service.profileMutation.Unlock()
	currentProfile, err := service.store.GetEndpointProfile(ctx, input.ID)
	switch {
	case errors.Is(err, store.ErrEndpointProfileNotFound):
		return Profile{}, ErrNotFound
	case err != nil:
		return Profile{}, err
	case currentProfile.Revision != input.ExpectedRevision:
		return Profile{}, ErrConflict
	}
	settings, err := service.store.GetSettings(ctx)
	if err != nil {
		return Profile{}, err
	}
	if settings.DefaultEndpointProfileID == input.ID {
		return Profile{}, ErrInUse
	}
	originalProfiles, err := service.reconcileCandidate(ctx, store.AgentEndpointProfile{
		ID: input.ID, Name: strings.TrimSpace(input.Name), RegistrationURL: strings.TrimSpace(input.RegistrationURL),
		QUICAddress: strings.TrimSpace(input.QUICAddress), Enabled: input.Enabled,
	}, input.Now)
	if err != nil {
		return Profile{}, err
	}
	updated, err := service.store.UpdateEndpointProfile(ctx, store.UpdateAgentEndpointProfileParams{
		ID: input.ID, Name: strings.TrimSpace(input.Name), RegistrationURL: strings.TrimSpace(input.RegistrationURL),
		QUICAddress:                  strings.TrimSpace(input.QUICAddress),
		RegistrationCACertificatePEM: strings.TrimSpace(input.RegistrationCACertificatePEM), Enabled: input.Enabled,
		ExpectedRevision: input.ExpectedRevision, ActorUserID: input.ActorUserID, Now: input.Now,
	})
	if err != nil {
		restoreErr := service.reconcileProfiles(ctx, originalProfiles, input.Now)
		if restoreErr != nil {
			return Profile{}, errors.Join(err, fmt.Errorf("restore Agent Listener certificate: %w", restoreErr))
		}
	}
	switch {
	case errors.Is(err, store.ErrEndpointProfileNotFound):
		return Profile{}, ErrNotFound
	case errors.Is(err, store.ErrEndpointProfileConflict):
		return Profile{}, ErrConflict
	case err != nil:
		return Profile{}, err
	default:
		return service.profileFromStore(updated), nil
	}
}

func (service *Service) DeleteProfile(ctx context.Context, id string) error {
	if !validation.IsUUID(id) {
		return ErrInvalidInput
	}
	service.profileMutation.Lock()
	defer service.profileMutation.Unlock()
	settings, err := service.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if settings.DefaultEndpointProfileID == id {
		return ErrInUse
	}
	originalProfiles, err := service.reconcileRemoval(ctx, id, time.Now().UTC())
	if err != nil {
		return err
	}
	err = service.store.DeleteEndpointProfile(ctx, id)
	if err != nil {
		restoreErr := service.reconcileProfiles(ctx, originalProfiles, time.Now().UTC())
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore Agent Listener certificate: %w", restoreErr))
		}
	}
	switch {
	case errors.Is(err, store.ErrEndpointProfileNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrEndpointProfileInUse):
		return ErrInUse
	default:
		return err
	}
}

func (service *Service) UpdateSettings(ctx context.Context, input SettingsInput) (Settings, error) {
	if !validation.IsUUID(input.ActorUserID) || input.ExpectedRevision <= 0 || input.Now.IsZero() ||
		!validAgentImage(input.AgentImage) ||
		!validAgentImage(input.ClusterTerminalImage) ||
		len(k8svalidation.IsDNS1123Label(strings.TrimSpace(input.AgentNamespace))) != 0 ||
		!allowedPullPolicy(input.AgentImagePullPolicy) {
		return Settings{}, ErrInvalidInput
	}
	service.profileMutation.Lock()
	defer service.profileMutation.Unlock()
	current, err := service.store.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if strings.TrimSpace(input.AgentNamespace) != current.AgentNamespace {
		hasEnrollments, checkErr := service.store.HasEnrollments(ctx)
		if checkErr != nil {
			return Settings{}, checkErr
		}
		if hasEnrollments {
			return Settings{}, ErrInUse
		}
	}
	updated, err := service.store.UpdateSettings(ctx, store.UpdatePlatformSettingsParams{
		AgentImage: strings.TrimSpace(input.AgentImage), AgentNamespace: strings.TrimSpace(input.AgentNamespace),
		AgentImagePullPolicy: input.AgentImagePullPolicy, ExpectedRevision: input.ExpectedRevision,
		ClusterTerminalImage: strings.TrimSpace(input.ClusterTerminalImage),
		ActorUserID:          input.ActorUserID, Now: input.Now,
	})
	if errors.Is(err, store.ErrPlatformSettingsConflict) {
		return Settings{}, ErrConflict
	}
	if err != nil {
		return Settings{}, err
	}
	return settingsFromStore(updated), nil
}

func (service *Service) ResolveEnrollmentSnapshot(ctx context.Context, profileID string) (Snapshot, error) {
	settings, err := service.store.GetSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(profileID) == "" {
		profileID = settings.DefaultEndpointProfileID
	}
	if !validation.IsUUID(profileID) {
		return Snapshot{}, ErrInvalidInput
	}
	profile, err := service.store.GetEndpointProfile(ctx, profileID)
	if errors.Is(err, store.ErrEndpointProfileNotFound) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	resolved := service.profileFromStore(profile)
	if resolved.Status != ProfileStatusReady {
		return Snapshot{}, ErrEndpointUnready
	}
	return Snapshot{
		EndpointProfileID: profile.ID, EndpointProfileRevision: profile.Revision,
		RegistrationURL: profile.RegistrationURL, QUICAddress: profile.QUICAddress,
		RegistrationCACertificatePEM: profile.RegistrationCACertificatePEM,
		AgentImage:                   settings.AgentImage, AgentNamespace: settings.AgentNamespace,
		AgentImagePullPolicy: settings.AgentImagePullPolicy,
	}, nil
}

func DesiredListenerSANs(ctx context.Context, settingsStore Store) ([]string, []string, error) {
	profiles, err := settingsStore.ListEndpointProfiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	return listenerSANs(profiles)
}

func listenerSANs(profiles []store.AgentEndpointProfile) ([]string, []string, error) {
	dnsNames := make([]string, 0, len(profiles))
	ipAddresses := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		host, _, splitErr := net.SplitHostPort(profile.QUICAddress)
		if splitErr != nil {
			return nil, nil, fmt.Errorf("stored Agent endpoint profile %q has invalid QUIC address: %w", profile.ID, splitErr)
		}
		if ip := net.ParseIP(host); ip != nil {
			ipAddresses = appendUnique(ipAddresses, ip.String())
		} else {
			dnsNames = appendUnique(dnsNames, strings.ToLower(host))
		}
	}
	return dnsNames, ipAddresses, nil
}

func (service *Service) profilesFromStore(items []store.AgentEndpointProfile) []Profile {
	result := make([]Profile, 0, len(items))
	for _, item := range items {
		result = append(result, service.profileFromStore(item))
	}
	return result
}

func (service *Service) profileFromStore(item store.AgentEndpointProfile) Profile {
	status := ProfileStatusDisabled
	if item.Enabled {
		status = ProfileStatusUnavailable
		host, _, err := net.SplitHostPort(item.QUICAddress)
		if err == nil && service.listenerCovers(host) {
			status = ProfileStatusReady
		}
	}
	return Profile{
		ID: item.ID, Name: item.Name, RegistrationURL: item.RegistrationURL,
		QUICAddress:                  item.QUICAddress,
		RegistrationCACertificatePEM: item.RegistrationCACertificatePEM,
		Enabled:                      item.Enabled, Status: status, Revision: item.Revision,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (service *Service) reconcileCandidate(
	ctx context.Context,
	candidate store.AgentEndpointProfile,
	now time.Time,
) ([]store.AgentEndpointProfile, error) {
	if service.reconcileListener == nil {
		return nil, nil
	}
	profiles, err := service.store.ListEndpointProfiles(ctx)
	if err != nil {
		return nil, err
	}
	originalProfiles := append([]store.AgentEndpointProfile(nil), profiles...)
	replaced := false
	for index := range profiles {
		if profiles[index].ID == candidate.ID {
			profiles[index] = candidate
			replaced = true
			break
		}
	}
	if !replaced {
		profiles = append(profiles, candidate)
	}
	if err := service.reconcileProfiles(ctx, profiles, now); err != nil {
		return nil, err
	}
	return originalProfiles, nil
}

func (service *Service) reconcileRemoval(
	ctx context.Context,
	profileID string,
	now time.Time,
) ([]store.AgentEndpointProfile, error) {
	if service.reconcileListener == nil {
		return nil, nil
	}
	profiles, err := service.store.ListEndpointProfiles(ctx)
	if err != nil {
		return nil, err
	}
	originalProfiles := append([]store.AgentEndpointProfile(nil), profiles...)
	desired := make([]store.AgentEndpointProfile, 0, len(profiles))
	found := false
	for _, profile := range profiles {
		if profile.ID == profileID {
			found = true
			continue
		}
		desired = append(desired, profile)
	}
	if !found {
		return nil, ErrNotFound
	}
	if err := service.reconcileProfiles(ctx, desired, now); err != nil {
		return nil, err
	}
	return originalProfiles, nil
}

func (service *Service) reconcileProfiles(
	ctx context.Context,
	profiles []store.AgentEndpointProfile,
	now time.Time,
) error {
	if service.reconcileListener == nil || profiles == nil {
		return nil
	}
	dnsNames, ipAddresses, err := listenerSANs(profiles)
	if err != nil {
		return err
	}
	return service.reconcileListener(ctx, dnsNames, ipAddresses, now)
}

func (service *Service) listenerCovers(host string) bool {
	data, err := os.ReadFile(service.listenerCertificateFile)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	return err == nil && certificate.VerifyHostname(host) == nil
}

func validateProfileInput(input ProfileInput, updating bool) error {
	if updating && (!validation.IsUUID(input.ID) || input.ExpectedRevision <= 0) {
		return invalidInput("端点版本无效，请刷新页面后重试")
	}
	if !validation.IsUUID(input.ActorUserID) || input.Now.IsZero() {
		return invalidInput("请求身份或时间无效")
	}
	if strings.TrimSpace(input.Name) == "" {
		return invalidInput("名称不能为空")
	}
	if len([]byte(strings.TrimSpace(input.Name))) > 128 {
		return invalidInput("名称不能超过 128 字节")
	}
	if strings.EqualFold(strings.TrimSpace(input.Name), "部署配置默认端点") {
		return invalidInput("该名称由 Server 部署配置保留")
	}
	registrationURL, err := url.Parse(strings.TrimSpace(input.RegistrationURL))
	if err != nil || registrationURL.Host == "" {
		return invalidInput("注册 URL 必须是包含主机名的完整 HTTP 或 HTTPS 地址")
	}
	if registrationURL.Scheme != "https" && registrationURL.Scheme != "http" {
		return invalidInput("注册 URL 只支持 HTTP 或 HTTPS")
	}
	if registrationURL.User != nil {
		return invalidInput("注册 URL 不能包含用户名或密码")
	}
	if registrationURL.RawQuery != "" || registrationURL.Fragment != "" ||
		(registrationURL.Path != "" && registrationURL.Path != "/") {
		return invalidInput("注册 URL 不能包含路径、查询参数或片段")
	}
	if registrationURL.Scheme == "http" && strings.TrimSpace(input.RegistrationCACertificatePEM) != "" {
		return invalidInput("HTTP 注册地址不能配置 HTTPS CA")
	}
	if strings.TrimSpace(input.RegistrationCACertificatePEM) != "" &&
		!validCertificatePEM(input.RegistrationCACertificatePEM) {
		return invalidInput("注册 HTTPS CA 必须是有效的 CA 证书 PEM")
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(input.QUICAddress))
	if err != nil || strings.TrimSpace(host) == "" {
		return invalidInput("QUIC 地址必须使用 host:port 格式")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return invalidInput("QUIC 端口必须介于 1 和 65535 之间")
	}
	return nil
}

func validCertificatePEM(value string) bool {
	block, rest := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return false
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	return err == nil && certificate.IsCA
}

func validAgentImage(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]byte(value)) <= 512 && !strings.ContainsAny(value, "\r\n\t ")
}

func allowedPullPolicy(value string) bool {
	return value == "Always" || value == "IfNotPresent" || value == "Never"
}

func settingsFromStore(item store.PlatformSettings) Settings {
	return Settings{
		DefaultEndpointProfileID: item.DefaultEndpointProfileID,
		AgentImage:               item.AgentImage, AgentNamespace: item.AgentNamespace,
		AgentImagePullPolicy: item.AgentImagePullPolicy, Revision: item.Revision,
		ClusterTerminalImage: item.ClusterTerminalImage,
		UpdatedAt:            item.UpdatedAt,
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
