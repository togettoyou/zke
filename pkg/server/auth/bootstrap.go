package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

type InitialAdminInput struct {
	Username    string
	DisplayName string
	Password    []byte
}

func CreateInitialAdmin(
	ctx context.Context,
	authStore *store.AuthStore,
	input InitialAdminInput,
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

	user, err := authStore.CreateInitialAdmin(ctx, store.InitialAdmin{
		UsernameNormalized: username,
		DisplayName:        displayName,
		PasswordHash:       passwordHash,
		RequestID:          requestID,
	})
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
