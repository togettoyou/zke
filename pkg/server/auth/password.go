package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	minimumPasswordCharacters = 15
	maximumArgon2MemoryKiB    = 256 * 1024
	maximumArgon2Iterations   = 10
	maximumArgon2Parallelism  = 16
	MaximumPasswordBytes      = 1024
)

type PasswordParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var defaultPasswordParams = PasswordParams{
	MemoryKiB:   64 * 1024,
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

func DefaultPasswordParams() PasswordParams {
	return defaultPasswordParams
}

func ValidateNewPassword(password []byte) error {
	if len(password) > MaximumPasswordBytes {
		return fmt.Errorf("password must not exceed %d bytes", MaximumPasswordBytes)
	}
	if !utf8.Valid(password) {
		return errors.New("password must be valid UTF-8")
	}
	if utf8.RuneCount(password) < minimumPasswordCharacters {
		return fmt.Errorf("password must contain at least %d characters", minimumPasswordCharacters)
	}
	return nil
}

func HashPassword(password []byte, params PasswordParams) (string, error) {
	if err := ValidateNewPassword(password); err != nil {
		return "", err
	}
	if err := params.validate(); err != nil {
		return "", err
	}

	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("generate password salt")
	}
	key := argon2.IDKey(
		password,
		salt,
		params.Iterations,
		params.MemoryKiB,
		params.Parallelism,
		params.KeyLength,
	)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.MemoryKiB,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(password []byte, encoded string) (matches bool, needsRehash bool, err error) {
	if len(password) > MaximumPasswordBytes {
		return false, false, fmt.Errorf("password must not exceed %d bytes", MaximumPasswordBytes)
	}
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, false, err
	}

	actual := argon2.IDKey(
		password,
		salt,
		params.Iterations,
		params.MemoryKiB,
		params.Parallelism,
		uint32(len(expected)),
	)
	matches = subtle.ConstantTimeCompare(actual, expected) == 1
	return matches, matches && params != DefaultPasswordParams(), nil
}

func parsePasswordHash(encoded string) (PasswordParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return PasswordParams{}, nil, nil, errors.New("password hash has an invalid format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil ||
		version != argon2.Version ||
		parts[2] != fmt.Sprintf("v=%d", version) {
		return PasswordParams{}, nil, nil, errors.New("password hash has an unsupported Argon2 version")
	}

	var params PasswordParams
	var parallelism uint32
	if _, err := fmt.Sscanf(
		parts[3],
		"m=%d,t=%d,p=%d",
		&params.MemoryKiB,
		&params.Iterations,
		&parallelism,
	); err != nil || parts[3] != fmt.Sprintf(
		"m=%d,t=%d,p=%d",
		params.MemoryKiB,
		params.Iterations,
		parallelism,
	) || parallelism > 255 {
		return PasswordParams{}, nil, nil, errors.New("password hash has invalid Argon2 parameters")
	}
	params.Parallelism = uint8(parallelism)

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return PasswordParams{}, nil, nil, errors.New("password hash has invalid salt encoding")
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return PasswordParams{}, nil, nil, errors.New("password hash has invalid key encoding")
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(expected))
	if err := params.validate(); err != nil {
		return PasswordParams{}, nil, nil, fmt.Errorf("password hash parameters are unsafe: %w", err)
	}

	return params, salt, expected, nil
}

func (params PasswordParams) validate() error {
	if params.MemoryKiB < 7*1024 || params.MemoryKiB > maximumArgon2MemoryKiB {
		return fmt.Errorf("Argon2 memory must be between %d and %d KiB", 7*1024, maximumArgon2MemoryKiB)
	}
	if params.Iterations == 0 || params.Iterations > maximumArgon2Iterations {
		return fmt.Errorf("Argon2 iterations must be between 1 and %d", maximumArgon2Iterations)
	}
	if params.Parallelism == 0 || params.Parallelism > maximumArgon2Parallelism {
		return fmt.Errorf("Argon2 parallelism must be between 1 and %d", maximumArgon2Parallelism)
	}
	if params.SaltLength < 16 || params.SaltLength > 64 {
		return errors.New("Argon2 salt length must be between 16 and 64 bytes")
	}
	if params.KeyLength < 16 || params.KeyLength > 64 {
		return errors.New("Argon2 key length must be between 16 and 64 bytes")
	}
	return nil
}
