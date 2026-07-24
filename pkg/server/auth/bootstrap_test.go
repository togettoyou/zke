package auth

import "testing"

func TestNormalizeUsername(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeUsername("  Ａdmin  ")
	if err != nil {
		t.Fatal(err)
	}
	if normalized != "admin" {
		t.Fatalf("normalized username = %q, want %q", normalized, "admin")
	}
}

func TestNormalizeUsernameRejectsControlCharacter(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeUsername("admin\nroot"); err == nil {
		t.Fatal("NormalizeUsername() accepted a control character")
	}
}
