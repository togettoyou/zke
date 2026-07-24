package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/auth"
)

func TestParseCreateAdminOptions(t *testing.T) {
	t.Parallel()

	options, err := parseCreateAdminOptions([]string{
		"--config", "server.yaml",
		"--username", "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.displayName != "admin" {
		t.Fatalf("display name = %q, want username default", options.displayName)
	}
}

func TestParseCreateAdminOptionsRequiresUsername(t *testing.T) {
	t.Parallel()

	if _, err := parseCreateAdminOptions([]string{"--config", "server.yaml"}); err == nil {
		t.Fatal("parseCreateAdminOptions() accepted a missing username")
	}
}

func TestReadPasswordFileRemovesLineEnding(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("a sufficiently long passphrase\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	password, err := readPasswordFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(password)
	if string(password) != "a sufficiently long passphrase" {
		t.Fatalf("password = %q, want line ending removed", password)
	}
}

func TestReadPasswordFileRejectsOversizedPassword(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "password")
	content := []byte(strings.Repeat("x", auth.MaximumPasswordBytes+1))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPasswordFile(path); err == nil {
		t.Fatal("readPasswordFile() accepted an oversized password")
	}
}
