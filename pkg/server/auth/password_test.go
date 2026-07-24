package auth

import (
	"strings"
	"testing"
)

var testPasswordParams = PasswordParams{
	MemoryKiB:   7 * 1024,
	Iterations:  1,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()

	password := []byte("a sufficiently long passphrase")
	encoded, err := HashPassword(password, testPasswordParams)
	if err != nil {
		t.Fatal(err)
	}

	matches, needsRehash, err := VerifyPassword(password, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("password did not match its Argon2id hash")
	}
	if !needsRehash {
		t.Fatal("test parameters were not marked for upgrade")
	}

	matches, _, err = VerifyPassword([]byte("a different long passphrase"), encoded)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("incorrect password matched")
	}
}

func TestHashPasswordUsesUniqueSalt(t *testing.T) {
	t.Parallel()

	password := []byte("a sufficiently long passphrase")
	first, err := HashPassword(password, testPasswordParams)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password, testPasswordParams)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password hashes reused a salt")
	}
}

func TestHashPasswordEnforcesNewPasswordPolicy(t *testing.T) {
	t.Parallel()

	if _, err := HashPassword([]byte("short"), testPasswordParams); err == nil {
		t.Fatal("HashPassword() accepted a password below the minimum length")
	}
	if _, err := HashPassword(
		[]byte(strings.Repeat("x", MaximumPasswordBytes+1)),
		testPasswordParams,
	); err == nil {
		t.Fatal("HashPassword() accepted an oversized password")
	}
}

func TestVerifyPasswordRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassword([]byte("a sufficiently long passphrase"), testPasswordParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyPassword(
		[]byte(strings.Repeat("x", MaximumPasswordBytes+1)),
		encoded,
	); err == nil {
		t.Fatal("VerifyPassword() accepted an oversized password")
	}
}

func TestVerifyPasswordRejectsUnsafeParameters(t *testing.T) {
	t.Parallel()

	encoded := "$argon2id$v=19$m=1048576,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZg"
	if _, _, err := VerifyPassword([]byte("a sufficiently long passphrase"), encoded); err == nil {
		t.Fatal("VerifyPassword() accepted excessive memory parameters")
	}
}

func TestVerifyPasswordRejectsMalformedVersion(t *testing.T) {
	t.Parallel()

	encoded := "$argon2id$v=19junk$m=7168,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZg"
	if _, _, err := VerifyPassword([]byte("a sufficiently long passphrase"), encoded); err == nil {
		t.Fatal("VerifyPassword() accepted a malformed version")
	}
}

func TestValidateNewPassword(t *testing.T) {
	t.Parallel()

	if err := ValidateNewPassword([]byte("short")); err == nil {
		t.Fatal("ValidateNewPassword() accepted a short password")
	}
	if err := ValidateNewPassword([]byte("允许使用空格和中文的安全口令示例")); err != nil {
		t.Fatalf("ValidateNewPassword() rejected Unicode: %v", err)
	}
	if err := ValidateNewPassword([]byte(strings.Repeat("x", MaximumPasswordBytes+1))); err == nil {
		t.Fatal("ValidateNewPassword() accepted an oversized password")
	}
}
