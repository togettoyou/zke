package customapplications

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	testProjectID = "00000000-0000-0000-0000-000000000101"
	testUserID    = "00000000-0000-0000-0000-000000000102"
)

type fakeStore struct {
	Store
	createInput store.CreateCustomApplicationParams
	created     store.CustomApplication
	err         error
}

func TestCreateAcceptsBase64RasterLogo(t *testing.T) {
	const logo = "data:image/jpeg;base64,/9j/2Q=="
	fake := &fakeStore{created: store.CustomApplication{
		ID: "00000000-0000-0000-0000-000000000103", ProjectID: testProjectID,
		Name: "Harbor", URL: "https://harbor.example.test/", LogoURL: logo,
	}}
	service, err := NewService(fake)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), testProjectID, testUserID, Input{
		Name: "Harbor", URL: "https://harbor.example.test/", LogoURL: logo,
		IdempotencyKey: "custom-app-create-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.createInput.LogoURL != logo {
		t.Fatalf("LogoURL = %q, want Base64 Data URL", fake.createInput.LogoURL)
	}
}

func TestCreateRejectsInvalidBase64Logos(t *testing.T) {
	service, _ := NewService(&fakeStore{})
	logos := []string{
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"data:image/jpeg;base64,not-valid-***",
		"data:image/jpeg;base64," + strings.Repeat("A", maxLogoDataURLBytes),
	}
	for _, logo := range logos {
		_, err := service.Create(context.Background(), testProjectID, testUserID, Input{
			Name: "bad", URL: "https://example.test", LogoURL: logo,
			IdempotencyKey: "custom-app-create-0001",
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create() logo error = %v, want invalid input", err)
		}
	}
}

func (fake *fakeStore) CreateCustomApplication(
	_ context.Context,
	input store.CreateCustomApplicationParams,
) (store.CustomApplication, error) {
	fake.createInput = input
	return fake.created, fake.err
}

func TestCreateNormalizesAndValidatesApplication(t *testing.T) {
	fake := &fakeStore{created: store.CustomApplication{
		ID: "00000000-0000-0000-0000-000000000103", ProjectID: testProjectID,
		Name: "Harbor", URL: "https://harbor.example.test/", LogoURL: "https://harbor.example.test/logo.svg",
	}}
	service, err := NewService(fake)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), testProjectID, testUserID, Input{
		Name: " Harbor ", URL: " https://harbor.example.test/ ",
		LogoURL:        " https://harbor.example.test/logo.svg ",
		IdempotencyKey: "custom-app-create-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.createInput.Name != "Harbor" || fake.createInput.URL != "https://harbor.example.test/" ||
		fake.createInput.LogoURL != "https://harbor.example.test/logo.svg" {
		t.Fatalf("input was not normalized: %#v", fake.createInput)
	}
}

func TestCreateRejectsUnsafeURLsBeforeStoreAccess(t *testing.T) {
	service, _ := NewService(&fakeStore{})
	for _, raw := range []string{"javascript:alert(1)", "//example.test", "https://user:pass@example.test"} {
		_, err := service.Create(context.Background(), testProjectID, testUserID, Input{
			Name: "bad", URL: raw, IdempotencyKey: "custom-app-create-0001",
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create(%q) error = %v, want invalid input", raw, err)
		}
	}
}

func TestCreateTranslatesIdempotencyConflict(t *testing.T) {
	service, _ := NewService(&fakeStore{err: store.ErrCustomApplicationIdempotencyConflict})
	_, err := service.Create(context.Background(), testProjectID, testUserID, Input{
		Name: "Harbor", URL: "https://harbor.example.test", IdempotencyKey: "custom-app-create-0001",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Create() error = %v, want idempotency conflict", err)
	}
}
