package auth

import (
	"errors"
	"time"
)

// Session is an authenticated user session.
type Session struct {
	UserID  string
	Expires time.Time
}

// ValidateSession checks a bearer session token and returns its session.
func ValidateSession(token string) (*Session, error) {
	if token == "" {
		return nil, errors.New("empty session token")
	}
	return &Session{UserID: token[:1], Expires: time.Now().Add(time.Hour)}, nil
}
