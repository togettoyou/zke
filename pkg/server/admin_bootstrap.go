package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/store"
)

const generatedAdminPasswordBytes = 32

func bootstrapInitialAdmin(
	ctx context.Context,
	authStore *store.AuthStore,
	config InitialAdminConfig,
	logger *slog.Logger,
) error {
	if !config.Enabled {
		return nil
	}
	hasUsers, err := authStore.HasUsers(ctx)
	if err != nil {
		return err
	}
	if hasUsers {
		logger.Info("initial administrator bootstrap skipped; users already exist")
		return nil
	}

	password, generated, err := loadInitialAdminPassword(config)
	if err != nil {
		return err
	}
	defer clear(password)
	user, err := auth.CreateInitialAdmin(
		ctx,
		authStore,
		auth.InitialAdminInput{
			Username:    config.Username,
			DisplayName: config.DisplayName,
			Password:    password,
		},
	)
	if errors.Is(err, store.ErrInitialAdminExists) {
		logger.Info(
			"initial administrator bootstrap skipped; another Server created it",
		)
		return nil
	}
	if err != nil {
		return fmt.Errorf("create initial administrator: %w", err)
	}
	attributes := []any{
		slog.String("user_id", user.ID),
		slog.String("username", user.Username),
	}
	if generated {
		attributes = append(
			attributes,
			slog.String("password_file", config.PasswordFile),
		)
	}
	logger.Info("initial administrator created", attributes...)
	return nil
}

func loadInitialAdminPassword(
	config InitialAdminConfig,
) ([]byte, bool, error) {
	password, err := readInitialAdminPasswordFile(config.PasswordFile)
	if err == nil {
		return password, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !config.AutoGeneratePassword {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(config.PasswordFile), 0o700); err != nil {
		return nil, false, fmt.Errorf(
			"create initial administrator password directory: %w",
			err,
		)
	}
	randomValue := make([]byte, generatedAdminPasswordBytes)
	if _, err := rand.Read(randomValue); err != nil {
		return nil, false, errors.New(
			"generate initial administrator password",
		)
	}
	password = []byte(base64.RawURLEncoding.EncodeToString(randomValue))
	clear(randomValue)
	file, err := os.OpenFile(
		config.PasswordFile,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if errors.Is(err, os.ErrExist) {
		clear(password)
		password, err = readInitialAdminPasswordFile(config.PasswordFile)
		return password, false, err
	}
	if err != nil {
		clear(password)
		return nil, false, fmt.Errorf(
			"create initial administrator password file: %w",
			err,
		)
	}
	value := append(append([]byte(nil), password...), '\n')
	if _, err := file.Write(value); err != nil {
		clear(value)
		clear(password)
		_ = file.Close()
		_ = os.Remove(config.PasswordFile)
		return nil, false, errors.New(
			"write initial administrator password file",
		)
	}
	clear(value)
	if err := file.Close(); err != nil {
		clear(password)
		_ = os.Remove(config.PasswordFile)
		return nil, false, errors.New(
			"close initial administrator password file",
		)
	}
	return password, true, nil
}

func readInitialAdminPasswordFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf(
			"open initial administrator password file: %w",
			err,
		)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, errors.New(
			"inspect initial administrator password file",
		)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New(
			"initial administrator password file must be a regular file",
		)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New(
			"initial administrator password file must not grant group or other access",
		)
	}

	password, err := io.ReadAll(
		io.LimitReader(file, auth.MaximumPasswordBytes+3),
	)
	if err != nil {
		return nil, errors.New(
			"read initial administrator password file",
		)
	}
	password = bytes.TrimSuffix(password, []byte("\r\n"))
	password = bytes.TrimSuffix(password, []byte("\n"))
	if len(password) > auth.MaximumPasswordBytes {
		clear(password)
		return nil, fmt.Errorf(
			"initial administrator password file exceeds %d password bytes",
			auth.MaximumPasswordBytes,
		)
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		clear(password)
		return nil, fmt.Errorf(
			"initial administrator password is invalid: %w",
			err,
		)
	}
	return password, nil
}
