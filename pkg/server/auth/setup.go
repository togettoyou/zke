package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/togettoyou/zke/pkg/server/store"
	"golang.org/x/text/unicode/norm"
)

const (
	maximumUsernameCharacters    = 128
	maximumDisplayNameCharacters = 200
)

var (
	ErrSetupAlreadyCompleted    = errors.New("a global administrator already exists")
	ErrSetupUsernameUnavailable = errors.New("setup username is unavailable")
	ErrInvalidSetupUsername     = errors.New("setup username is invalid")
)

type FirstGlobalAdministratorInput struct {
	Username    string
	DisplayName string
	Password    []byte
}

type AdministratorSetupInput struct {
	Username  string
	Password  []byte
	RequestID string
}

func (service *Service) SetupRequired(ctx context.Context) (bool, error) {
	hasAdministrator, err := service.store.HasGlobalAdministrator(ctx)
	if err != nil {
		return false, err
	}
	return !hasAdministrator, nil
}

func (service *Service) SetupAdministrator(
	ctx context.Context,
	input AdministratorSetupInput,
) (User, error) {
	if strings.TrimSpace(input.RequestID) == "" {
		return User{}, errors.New("setup request ID is required")
	}
	required, err := service.SetupRequired(ctx)
	if err != nil {
		return User{}, err
	}
	if !required {
		return User{}, ErrSetupAlreadyCompleted
	}
	username, err := NormalizeUsername(input.Username)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrInvalidSetupUsername, err)
	}
	displayName, err := normalizeDisplayName(input.Username)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrInvalidSetupUsername, err)
	}
	if err := ValidateNewPassword(input.Password); err != nil {
		return User{}, ErrInvalidNewPassword
	}
	passwordHash, err := service.hashPassword(
		ctx,
		input.Password,
		service.passwordParams,
	)
	if err != nil {
		return User{}, err
	}
	return createFirstGlobalAdministrator(ctx, service.store, store.FirstGlobalAdministrator{
		UsernameNormalized: username,
		DisplayName:        displayName,
		PasswordHash:       passwordHash,
		RequestID:          input.RequestID,
	})
}

func CreateFirstGlobalAdministrator(
	ctx context.Context,
	authStore *store.AuthStore,
	input FirstGlobalAdministratorInput,
) (User, error) {
	username, err := NormalizeUsername(input.Username)
	if err != nil {
		return User{}, err
	}
	displayName, err := normalizeDisplayName(input.DisplayName)
	if err != nil {
		return User{}, err
	}
	if err := ValidateNewPassword(input.Password); err != nil {
		return User{}, err
	}

	passwordHash, err := HashPassword(input.Password, DefaultPasswordParams())
	if err != nil {
		return User{}, err
	}
	requestID, err := newInitializationRequestID()
	if err != nil {
		return User{}, err
	}

	return createFirstGlobalAdministrator(ctx, authStore, store.FirstGlobalAdministrator{
		UsernameNormalized: username,
		DisplayName:        displayName,
		PasswordHash:       passwordHash,
		RequestID:          requestID,
	})
}

func createFirstGlobalAdministrator(
	ctx context.Context,
	authStore Store,
	input store.FirstGlobalAdministrator,
) (User, error) {
	user, err := authStore.CreateFirstGlobalAdministrator(ctx, input)
	if errors.Is(err, store.ErrGlobalAdministratorExists) {
		return User{}, ErrSetupAlreadyCompleted
	}
	if errors.Is(err, store.ErrGlobalAdministratorUsernameUnavailable) {
		return User{}, ErrSetupUsernameUnavailable
	}
	if err != nil {
		return User{}, err
	}
	return User{
		ID:          user.ID,
		Username:    user.UsernameNormalized,
		DisplayName: user.DisplayName,
	}, nil
}

func NormalizeUsername(username string) (string, error) {
	normalized := strings.ToLower(norm.NFKC.String(strings.TrimSpace(username)))
	if normalized == "" {
		return "", errors.New("username is required")
	}
	if utf8.RuneCountInString(normalized) > maximumUsernameCharacters {
		return "", errors.New("username is too long")
	}
	for _, value := range normalized {
		if unicode.IsControl(value) {
			return "", errors.New("username must not contain control characters")
		}
	}
	return normalized, nil
}

func normalizeDisplayName(displayName string) (string, error) {
	normalized := norm.NFKC.String(strings.TrimSpace(displayName))
	if normalized == "" {
		return "", errors.New("display name is required")
	}
	if utf8.RuneCountInString(normalized) > maximumDisplayNameCharacters {
		return "", errors.New("display name is too long")
	}
	for _, value := range normalized {
		if unicode.IsControl(value) {
			return "", errors.New("display name must not contain control characters")
		}
	}
	return normalized, nil
}

func newInitializationRequestID() (string, error) {
	var randomValue [16]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		return "", errors.New("generate initialization request ID")
	}
	return "admin-init-" + hex.EncodeToString(randomValue[:]), nil
}
