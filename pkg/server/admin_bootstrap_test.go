package server

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadInitialAdminPasswordGeneratesOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials", "admin-password")
	config := InitialAdminConfig{
		PasswordFile:         path,
		AutoGeneratePassword: true,
	}
	first, generated, err := loadInitialAdminPassword(config)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first)
	if !generated {
		t.Fatal("initial password was not reported as generated")
	}
	if len(first) != 43 {
		t.Fatalf("generated password length = %d, want 43", len(first))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("generated password mode = %o, want 600", info.Mode().Perm())
	}

	second, generated, err := loadInitialAdminPassword(config)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second)
	if generated {
		t.Fatal("existing initial password was reported as generated")
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second load returned a different initial password")
	}
}

func TestReadInitialAdminPasswordFileRemovesLineEnding(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(
		path,
		[]byte("a sufficiently long passphrase\r\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	password, err := readInitialAdminPasswordFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(password)
	if string(password) != "a sufficiently long passphrase" {
		t.Fatalf("unexpected initial administrator password")
	}
}

func TestLoadInitialAdminPasswordRequiresExistingFileWhenGenerationDisabled(
	t *testing.T,
) {
	t.Parallel()

	_, _, err := loadInitialAdminPassword(InitialAdminConfig{
		PasswordFile: filepath.Join(t.TempDir(), "missing"),
	})
	if err == nil {
		t.Fatal("missing initial password file was accepted")
	}
}

func TestReadInitialAdminPasswordFileRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not available")
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(
		path,
		[]byte("a sufficiently long passphrase\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := readInitialAdminPasswordFile(path); err == nil {
		t.Fatal("broad initial password file permissions were accepted")
	}
}
