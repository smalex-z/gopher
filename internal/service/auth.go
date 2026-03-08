package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"golang.org/x/crypto/bcrypt"
)

const sessionDuration = 24 * time.Hour

type session struct {
	expiresAt time.Time
}

type AuthService struct {
	mu       sync.RWMutex
	sessions map[string]session
}

func NewAuthService() *AuthService {
	return &AuthService{sessions: make(map[string]session)}
}

func (s *AuthService) IsSetup() (bool, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return false, err
	}
	return settings.IsSetup, nil
}

func (s *AuthService) Setup(password string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	if settings.IsSetup {
		return fmt.Errorf("already configured")
	}
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	settings.PasswordHash = string(hash)
	settings.IsSetup = true
	return db.SaveSettings(settings)
}

func (s *AuthService) Login(password string) (string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	if !settings.IsSetup {
		return "", fmt.Errorf("not configured")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(settings.PasswordHash), []byte(password)); err != nil {
		return "", fmt.Errorf("invalid password")
	}
	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate session: %w", err)
	}
	s.mu.Lock()
	s.sessions[token] = session{expiresAt: time.Now().Add(sessionDuration)}
	s.mu.Unlock()
	return token, nil
}

func (s *AuthService) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// ValidateSession returns true and slides the expiry window if the token is valid.
func (s *AuthService) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok || time.Now().After(sess.expiresAt) {
		delete(s.sessions, token)
		return false
	}
	s.sessions[token] = session{expiresAt: time.Now().Add(sessionDuration)}
	return true
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
