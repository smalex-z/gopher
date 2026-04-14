package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionDuration     = 24 * time.Hour
	pendingTOTPDuration = 5 * time.Minute
	auditLogCapacity    = 200
)

// ─── Audit log ───────────────────────────────────────────────────────────────

type AuditEvent struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	IP    string    `json:"ip"`
}

type auditRing struct {
	mu     sync.Mutex
	events [auditLogCapacity]AuditEvent
	next   int
	count  int
}

func (a *auditRing) add(event, ip string) {
	a.mu.Lock()
	a.events[a.next] = AuditEvent{Time: time.Now(), Event: event, IP: ip}
	a.next = (a.next + 1) % auditLogCapacity
	if a.count < auditLogCapacity {
		a.count++
	}
	a.mu.Unlock()
}

// Snapshot returns events newest-first.
func (a *auditRing) Snapshot() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEvent, a.count)
	for i := range out {
		// Walk backwards from last written slot.
		idx := (a.next - 1 - i + auditLogCapacity) % auditLogCapacity
		out[i] = a.events[idx]
	}
	return out
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

type session struct {
	expiresAt time.Time
}

type pendingTOTPEntry struct {
	expiresAt time.Time
}

// LoginResult is returned by Login.
type LoginResult struct {
	Token        string // session token (empty if NeedsTOTP)
	NeedsTOTP    bool
	PendingToken string // short-lived proof-of-password for the TOTP step
}

type AuthService struct {
	mu          sync.RWMutex
	sessions    map[string]session
	pendingTOTP map[string]pendingTOTPEntry
	rl          *loginRateLimiter
	audit       auditRing
}

func NewAuthService() *AuthService {
	return &AuthService{
		sessions:    make(map[string]session),
		pendingTOTP: make(map[string]pendingTOTPEntry),
		rl:          newLoginRateLimiter(),
	}
}

func (s *AuthService) RateLimiter() *loginRateLimiter { return s.rl }

// AuditLog returns recent auth events, newest first.
func (s *AuthService) AuditLog() []AuditEvent { return s.audit.Snapshot() }

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

// Login validates the password. Returns a session token immediately when 2FA is
// disabled, or a short-lived pending token when TOTP is still required.
func (s *AuthService) Login(password, ip string) (LoginResult, error) {
	if !s.rl.record(ip) {
		s.logEvent("LOGIN_RATE_LIMITED", ip)
		return LoginResult{}, fmt.Errorf("too many attempts")
	}

	settings, err := db.GetSettings()
	if err != nil {
		return LoginResult{}, err
	}
	if !settings.IsSetup {
		return LoginResult{}, fmt.Errorf("not configured")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(settings.PasswordHash), []byte(password)); err != nil {
		s.logEvent("LOGIN_FAILED", ip)
		return LoginResult{}, fmt.Errorf("invalid password")
	}

	if settings.TOTPEnabled {
		pending, err := generateToken()
		if err != nil {
			return LoginResult{}, fmt.Errorf("failed to generate pending token: %w", err)
		}
		s.mu.Lock()
		s.pendingTOTP[pending] = pendingTOTPEntry{expiresAt: time.Now().Add(pendingTOTPDuration)}
		s.mu.Unlock()
		return LoginResult{NeedsTOTP: true, PendingToken: pending}, nil
	}

	token, err := s.createSession()
	if err != nil {
		return LoginResult{}, err
	}
	s.rl.Reset(ip) // clear attempts after successful login
	s.logEvent("LOGIN_SUCCESS", ip)
	return LoginResult{Token: token}, nil
}

// LoginTOTP completes the 2FA step after a successful password check.
func (s *AuthService) LoginTOTP(pendingToken, code, ip string) (string, error) {
	s.mu.Lock()
	entry, ok := s.pendingTOTP[pendingToken]
	if ok {
		delete(s.pendingTOTP, pendingToken)
	}
	s.mu.Unlock()

	if !ok || time.Now().After(entry.expiresAt) {
		s.logEvent("LOGIN_FAILED_2FA_EXPIRED", ip)
		return "", fmt.Errorf("invalid or expired token")
	}

	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}

	if verifyTOTP(settings.TOTPSecret, code) {
		token, err := s.createSession()
		if err != nil {
			return "", err
		}
		s.rl.Reset(ip)
		s.logEvent("LOGIN_SUCCESS", ip)
		return token, nil
	}

	matched, updatedCodes, err := verifyAndConsumeBackupCode(settings.TOTPBackupCodes, code)
	if err != nil {
		return "", fmt.Errorf("backup code check failed: %w", err)
	}
	if matched {
		settings.TOTPBackupCodes = updatedCodes
		if saveErr := db.SaveSettings(settings); saveErr != nil {
			log.Printf("WARN: failed to save consumed backup code: %v", saveErr)
		}
		token, err := s.createSession()
		if err != nil {
			return "", err
		}
		s.rl.Reset(ip)
		s.logEvent("LOGIN_SUCCESS_BACKUP_CODE", ip)
		return token, nil
	}

	s.logEvent("LOGIN_FAILED_TOTP", ip)
	return "", fmt.Errorf("invalid code")
}

func (s *AuthService) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

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

// ─── 2FA management ──────────────────────────────────────────────────────────

func (s *AuthService) TOTPStatus() (enabled bool, backupCodesRemaining int, err error) {
	settings, err := db.GetSettings()
	if err != nil {
		return false, 0, err
	}
	codes, _ := unmarshalBackupCodes(settings.TOTPBackupCodes)
	return settings.TOTPEnabled, len(codes), nil
}

func (s *AuthService) TOTPEnroll() (secret, qrDataURL string, err error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", "", err
	}
	secret, qrDataURL, err = generateTOTPSecret("admin")
	if err != nil {
		return "", "", err
	}
	settings.TOTPSecret = secret
	settings.TOTPEnabled = false
	if err := db.SaveSettings(settings); err != nil {
		return "", "", fmt.Errorf("failed to save pending TOTP secret: %w", err)
	}
	return secret, qrDataURL, nil
}

func (s *AuthService) TOTPConfirm(code string) ([]string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	if settings.TOTPSecret == "" {
		return nil, fmt.Errorf("no enrollment in progress; call enroll first")
	}
	if !verifyTOTP(settings.TOTPSecret, code) {
		return nil, fmt.Errorf("invalid code")
	}
	plain, hashed, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	codesJSON, err := marshalBackupCodes(hashed)
	if err != nil {
		return nil, err
	}
	settings.TOTPEnabled = true
	settings.TOTPBackupCodes = codesJSON
	if err := db.SaveSettings(settings); err != nil {
		return nil, fmt.Errorf("failed to enable 2FA: %w", err)
	}
	return plain, nil
}

func (s *AuthService) TOTPDisable(code string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	if !settings.TOTPEnabled {
		return fmt.Errorf("2FA is not enabled")
	}
	valid := verifyTOTP(settings.TOTPSecret, code)
	if !valid {
		matched, _, err := verifyAndConsumeBackupCode(settings.TOTPBackupCodes, code)
		if err != nil {
			return err
		}
		valid = matched
	}
	if !valid {
		return fmt.Errorf("invalid code")
	}
	settings.TOTPEnabled = false
	settings.TOTPSecret = ""
	settings.TOTPBackupCodes = ""
	return db.SaveSettings(settings)
}

func (s *AuthService) TOTPRegenerateBackupCodes(code string) ([]string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return nil, err
	}
	if !settings.TOTPEnabled {
		return nil, fmt.Errorf("2FA is not enabled")
	}
	if !verifyTOTP(settings.TOTPSecret, code) {
		return nil, fmt.Errorf("invalid code")
	}
	plain, hashed, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	codesJSON, err := marshalBackupCodes(hashed)
	if err != nil {
		return nil, err
	}
	settings.TOTPBackupCodes = codesJSON
	return plain, db.SaveSettings(settings)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (s *AuthService) createSession() (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate session: %w", err)
	}
	s.mu.Lock()
	s.sessions[token] = session{expiresAt: time.Now().Add(sessionDuration)}
	s.mu.Unlock()
	return token, nil
}

func (s *AuthService) logEvent(event, ip string) {
	s.audit.add(event, ip)
	log.Printf("gopher-auth: %s ip=%s", event, ip)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// logAuthEvent is kept for external callers (e.g. bootstrap) that don't have
// an AuthService reference.
func logAuthEvent(event, ip string) {
	log.Printf("gopher-auth: %s ip=%s", event, ip)
}
