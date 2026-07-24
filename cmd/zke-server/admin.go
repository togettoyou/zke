package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/togettoyou/zke/pkg/server"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
	"golang.org/x/term"
)

type createAdminOptions struct {
	configPath   string
	username     string
	displayName  string
	passwordFile string
}

const adminCreationTimeout = 30 * time.Second

func runCreateAdmin(args []string) error {
	options, err := parseCreateAdminOptions(args)
	if err != nil {
		return err
	}
	cfg, err := server.LoadConfig([]string{"--config", options.configPath})
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	password, err := readAdminPassword(options.passwordFile, os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	defer clear(password)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseContext, cancelDatabase := context.WithTimeout(ctx, cfg.Database.ConnectTimeout)
	database, err := store.Open(databaseContext, cfg.Database.URL)
	cancelDatabase()
	if err != nil {
		return err
	}
	defer database.Close()

	migrationContext, cancelMigration := context.WithTimeout(ctx, cfg.Database.MigrationTimeout)
	_, err = migrations.Apply(migrationContext, database)
	cancelMigration()
	if err != nil {
		return fmt.Errorf("migrate PostgreSQL database: %w", err)
	}

	adminContext, cancelAdmin := context.WithTimeout(ctx, adminCreationTimeout)
	user, err := auth.CreateInitialAdmin(adminContext, store.NewAuthStore(database), auth.InitialAdminInput{
		Username:    options.username,
		DisplayName: options.displayName,
		Password:    password,
	})
	cancelAdmin()
	if err != nil {
		return fmt.Errorf("create initial administrator: %w", err)
	}

	fmt.Fprintf(os.Stdout, "created initial administrator %s (%s)\n", user.UsernameNormalized, user.ID)
	return nil
}

func parseCreateAdminOptions(args []string) (createAdminOptions, error) {
	var options createAdminOptions
	flags := flag.NewFlagSet("zke-server create-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.configPath, "config", "", "path to the ZKE Server YAML configuration")
	flags.StringVar(&options.username, "username", "", "initial administrator username")
	flags.StringVar(&options.displayName, "display-name", "", "initial administrator display name")
	flags.StringVar(&options.passwordFile, "password-file", "", "path to a file containing the password")
	if err := flags.Parse(args); err != nil {
		return createAdminOptions{}, fmt.Errorf("parse create-admin flags: %w", err)
	}
	if flags.NArg() != 0 {
		return createAdminOptions{}, fmt.Errorf(
			"unexpected create-admin arguments: %s",
			strings.Join(flags.Args(), " "),
		)
	}
	if strings.TrimSpace(options.configPath) == "" {
		return createAdminOptions{}, errors.New("--config is required")
	}
	if strings.TrimSpace(options.username) == "" {
		return createAdminOptions{}, errors.New("--username is required")
	}
	if strings.TrimSpace(options.displayName) == "" {
		options.displayName = options.username
	}

	return options, nil
}

func readAdminPassword(passwordFile string, input *os.File, output io.Writer) ([]byte, error) {
	if passwordFile != "" {
		return readPasswordFile(passwordFile)
	}
	if !term.IsTerminal(int(input.Fd())) {
		return nil, errors.New("interactive password input requires a terminal; use --password-file")
	}

	fmt.Fprint(output, "Password: ")
	password, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(output)
	if err != nil {
		return nil, errors.New("read administrator password")
	}
	fmt.Fprint(output, "Confirm password: ")
	confirmation, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(output)
	if err != nil {
		clear(password)
		return nil, errors.New("read administrator password confirmation")
	}
	defer clear(confirmation)
	if !bytes.Equal(password, confirmation) {
		clear(password)
		return nil, errors.New("administrator passwords do not match")
	}
	return password, nil
}

func readPasswordFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open password file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect password file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("password file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("password file permissions must not grant group or other access")
	}

	password, err := io.ReadAll(io.LimitReader(file, auth.MaximumPasswordBytes+3))
	if err != nil {
		return nil, fmt.Errorf("read password file: %w", err)
	}
	password = bytes.TrimSuffix(password, []byte("\r\n"))
	password = bytes.TrimSuffix(password, []byte("\n"))
	if len(password) > auth.MaximumPasswordBytes {
		clear(password)
		return nil, fmt.Errorf("password file exceeds %d password bytes", auth.MaximumPasswordBytes)
	}
	return password, nil
}
