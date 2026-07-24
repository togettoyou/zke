package auth

import "time"

type User struct {
	ID          string
	Username    string
	DisplayName string
}

type Identity struct {
	User      User
	SessionID string
	ExpiresAt time.Time

	csrfTokenDigest []byte
}

type LoginResult struct {
	User         User
	SessionID    string
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}
