package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionDuration     = 24 * time.Hour
	pendingTOTPDuration = 5 * time.Minute
	auditLogLimit       = 200
)

// ─── Audit log ───────────────────────────────────────────────────────────────
//
// The audit log was previously an in-memory ring buffer that lost everything
// on restart. It now writes through to the unified `events` table (source="auth")
// and the AuditLog() query filters back out by source. The AuditEvent shape is
// preserved so existing handlers and the security page UI keep working.

type AuditEvent struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	IP    string    `json:"ip"`
}

// authEventSeverity classifies auth events into the unified-events severity
// scale. SUCCESS-y events are info; everything else is warn so the dashboard
// has a stable signal of "something interesting happened."
func authEventSeverity(event string) string {
	if strings.HasPrefix(event, "LOGIN_SUCCESS") {
		return "info"
	}
	return "warn"
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
}

func NewAuthService() *AuthService {
	return &AuthService{
		sessions:    make(map[string]session),
		pendingTOTP: make(map[string]pendingTOTPEntry),
		rl:          newLoginRateLimiter(),
	}
}

func (s *AuthService) RateLimiter() *loginRateLimiter { return s.rl }

// AuditLog returns recent auth events, newest first. Reads from the unified
// events table filtered to source=auth and projects rows back to the
// AuditEvent shape the dashboard already expects.
func (s *AuthService) AuditLog() []AuditEvent {
	rows, err := db.GetEvents(db.EventFilter{Source: "auth", Limit: auditLogLimit})
	if err != nil {
		log.Printf("auth: audit log query failed: %v", err)
		return nil
	}
	out := make([]AuditEvent, len(rows))
	for i, r := range rows {
		out[i] = AuditEvent{Time: r.CreatedAt, Event: r.Kind, IP: r.IP}
	}
	return out
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
//
// Rate-limited per-IP for defense in depth. The pendingToken is already
// single-use (deleted on first lookup), so the practical brute-force ceiling
// is bounded by the Login rate limiter alone — but stacking the limiter
// here too means a TOTP attempt directly debits the IP's bucket instead of
// only via the upstream Login that minted the pending token.
func (s *AuthService) LoginTOTP(pendingToken, code, ip string) (string, error) {
	if !s.rl.record(ip) {
		s.logEvent("LOGIN_TOTP_RATE_LIMITED", ip)
		return "", fmt.Errorf("too many attempts")
	}

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

	// Try every enrolled device. First match wins.
	if deviceID, ok := verifyTOTPAcrossDevices(code); ok {
		if err := db.TouchTOTPDevice(deviceID); err != nil {
			log.Printf("WARN: failed to update last_used_at for device %s: %v", deviceID, err)
		}
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

// SensitiveOpChallenge carries the credential the operator submits to confirm
// a sensitive action (e.g. private key download). Either a TOTP code (when
// 2FA is enrolled) or the login password (when it isn't); the server picks
// based on settings.TOTPEnabled.
type SensitiveOpChallenge struct {
	TOTPCode string `json:"totp_code,omitempty"`
	Password string `json:"password,omitempty"`
}

// VerifySensitiveOp gates a privileged operation behind a fresh re-auth, so a
// stolen session cookie alone isn't enough to exfiltrate, say, an SSH private
// key. Reuses the login rate limiter (per-IP) so an attacker can't brute-force
// either the password fallback or the TOTP code.
//
// 2FA enrolled → TOTP code (or backup code) required.
// 2FA not enrolled → login password required.
//
// All attempts are written to the audit log so the operator can see if
// something tried to grab their keys.
func (s *AuthService) VerifySensitiveOp(req SensitiveOpChallenge, ip string) error {
	if !s.rl.record(ip) {
		s.logEvent("SENSITIVE_OP_RATE_LIMITED", ip)
		return fmt.Errorf("too many attempts; try again later")
	}

	settings, err := db.GetSettings()
	if err != nil {
		return err
	}

	if settings.TOTPEnabled {
		if req.TOTPCode == "" {
			return fmt.Errorf("totp_code required")
		}
		// Try active TOTP devices first, then backup codes (consuming the
		// matched backup code on success — same semantics as login).
		_, updatedBackup, ok := verifyTOTPOrBackup(req.TOTPCode, settings.TOTPBackupCodes)
		if !ok {
			s.logEvent("SENSITIVE_OP_FAILED_2FA", ip)
			return fmt.Errorf("invalid code")
		}
		if updatedBackup != settings.TOTPBackupCodes {
			settings.TOTPBackupCodes = updatedBackup
			if err := db.SaveSettings(settings); err != nil {
				log.Printf("WARN: persist consumed backup code (sensitive op): %v", err)
			}
		}
	} else {
		if req.Password == "" {
			return fmt.Errorf("password required")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(settings.PasswordHash), []byte(req.Password)); err != nil {
			s.logEvent("SENSITIVE_OP_FAILED_PASSWORD", ip)
			return fmt.Errorf("invalid password")
		}
	}

	s.rl.Reset(ip)
	return nil
}

// SensitiveOpRequirement tells the dashboard which credential to prompt for.
// "totp" when 2FA is on, "password" otherwise. Used by the modal that wraps
// downloads / other sensitive ops so it can render the right input field.
func (s *AuthService) SensitiveOpRequirement() (string, error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", err
	}
	if settings.TOTPEnabled {
		return "totp", nil
	}
	return "password", nil
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

// TOTPDeviceInfo is the safe-to-serialise view of an enrolled device.
type TOTPDeviceInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// verifyTOTPAcrossDevices walks every enrolled device and returns the matching
// device ID on the first hit. Caller is responsible for updating last_used_at.
func verifyTOTPAcrossDevices(code string) (string, bool) {
	devices, err := db.GetTOTPDevices()
	if err != nil {
		return "", false
	}
	for _, d := range devices {
		if verifyTOTP(d.Secret, code) {
			return d.ID, true
		}
	}
	return "", false
}

// verifyTOTPOrBackup matches a code against any device first, then backup codes.
// Returns (deviceID, backupConsumedJSON, ok). If a backup code matched, the caller
// must persist the updated AppSettings.TOTPBackupCodes JSON.
func verifyTOTPOrBackup(code, backupCodesJSON string) (deviceID, updatedBackupJSON string, ok bool) {
	if id, hit := verifyTOTPAcrossDevices(code); hit {
		return id, backupCodesJSON, true
	}
	matched, updated, err := verifyAndConsumeBackupCode(backupCodesJSON, code)
	if err != nil || !matched {
		return "", backupCodesJSON, false
	}
	return "", updated, true
}

func (s *AuthService) TOTPStatus() (enabled bool, devices []TOTPDeviceInfo, backupCodesRemaining int, err error) {
	settings, err := db.GetSettings()
	if err != nil {
		return false, nil, 0, err
	}
	rows, err := db.GetTOTPDevices()
	if err != nil {
		return false, nil, 0, err
	}
	devices = make([]TOTPDeviceInfo, 0, len(rows))
	for _, d := range rows {
		devices = append(devices, TOTPDeviceInfo{
			ID:         d.ID,
			Name:       d.Name,
			CreatedAt:  d.CreatedAt,
			LastUsedAt: d.LastUsedAt,
		})
	}
	codes, _ := unmarshalBackupCodes(settings.TOTPBackupCodes)
	return len(devices) > 0, devices, len(codes), nil
}

// TOTPEnroll generates a fresh TOTP secret for a *new* device and stashes it
// in AppSettings.TOTPSecret as the pending enrollment slot. The secret moves
// into the totp_devices table on Confirm.
func (s *AuthService) TOTPEnroll() (secret, qrDataURL string, err error) {
	settings, err := db.GetSettings()
	if err != nil {
		return "", "", err
	}
	count, err := db.CountTOTPDevices()
	if err != nil {
		return "", "", err
	}
	// Disambiguate the QR account_name across devices: same issuer, but a
	// numeric suffix on the account name so password managers don't collapse them.
	accountName := "admin"
	if count > 0 {
		accountName = fmt.Sprintf("admin (device %d)", count+1)
	}
	secret, qrDataURL, err = generateTOTPSecret(accountName)
	if err != nil {
		return "", "", err
	}
	settings.TOTPSecret = secret
	if err := db.SaveSettings(settings); err != nil {
		return "", "", fmt.Errorf("failed to save pending TOTP secret: %w", err)
	}
	return secret, qrDataURL, nil
}

// TOTPConfirm finalises an enrollment: verifies the code against the pending
// secret, persists a new TOTPDevice with the given name, clears the pending
// slot, and (only if this is the first device) generates backup codes.
// Returns plaintext backup codes only on first enrollment; nil otherwise.
func (s *AuthService) TOTPConfirm(code, name string) ([]string, error) {
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

	deviceName := strings.TrimSpace(name)
	if deviceName == "" {
		deviceName = "Authenticator"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}

	device := &db.TOTPDevice{
		ID:        randomDeviceID(),
		Name:      deviceName,
		Secret:    settings.TOTPSecret,
		CreatedAt: time.Now(),
	}
	if err := db.CreateTOTPDevice(device); err != nil {
		return nil, fmt.Errorf("failed to save device: %w", err)
	}

	// Clear the pending slot.
	settings.TOTPSecret = ""

	// Generate backup codes only if this is the first device. Otherwise keep
	// the existing set; backup codes are shared across devices.
	var plain []string
	count, err := db.CountTOTPDevices()
	if err != nil {
		return nil, err
	}
	if count == 1 || settings.TOTPBackupCodes == "" {
		var hashed []string
		plain, hashed, err = generateBackupCodes()
		if err != nil {
			return nil, err
		}
		codesJSON, err := marshalBackupCodes(hashed)
		if err != nil {
			return nil, err
		}
		settings.TOTPBackupCodes = codesJSON
	}

	settings.TOTPEnabled = true
	if err := db.SaveSettings(settings); err != nil {
		return nil, fmt.Errorf("failed to persist enrollment: %w", err)
	}
	return plain, nil
}

// TOTPDisable removes ALL devices and clears backup codes. Requires a valid
// code from any device or a backup code.
func (s *AuthService) TOTPDisable(code string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	if !settings.TOTPEnabled {
		return fmt.Errorf("2FA is not enabled")
	}
	_, _, ok := verifyTOTPOrBackup(code, settings.TOTPBackupCodes)
	if !ok {
		return fmt.Errorf("invalid code")
	}
	if err := db.DeleteAllTOTPDevices(); err != nil {
		return fmt.Errorf("failed to delete devices: %w", err)
	}
	settings.TOTPEnabled = false
	settings.TOTPSecret = ""
	settings.TOTPBackupCodes = ""
	return db.SaveSettings(settings)
}

// TOTPRemoveDevice removes a single device. Requires a valid code from any
// enrolled device (including the one being removed — the code authenticates
// the action, not the device) or a backup code. If this leaves zero devices,
// 2FA is disabled and backup codes are cleared.
func (s *AuthService) TOTPRemoveDevice(deviceID, code string) error {
	settings, err := db.GetSettings()
	if err != nil {
		return err
	}
	if !settings.TOTPEnabled {
		return fmt.Errorf("2FA is not enabled")
	}
	if _, err := db.GetTOTPDevice(deviceID); err != nil {
		return err
	}

	_, updatedBackup, ok := verifyTOTPOrBackup(code, settings.TOTPBackupCodes)
	if !ok {
		return fmt.Errorf("invalid code")
	}
	settings.TOTPBackupCodes = updatedBackup

	if err := db.DeleteTOTPDevice(deviceID); err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}

	count, err := db.CountTOTPDevices()
	if err != nil {
		return err
	}
	if count == 0 {
		// Last device removed — disable 2FA entirely.
		settings.TOTPEnabled = false
		settings.TOTPSecret = ""
		settings.TOTPBackupCodes = ""
	}
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
	if _, ok := verifyTOTPAcrossDevices(code); !ok {
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

// randomDeviceID returns 16 hex chars — same scheme as other Gopher IDs.
//
// Panics on crypto/rand failure rather than silently returning the zero
// byte slice. On Linux post-boot this never fires; if it does, the system
// has no entropy and nothing involving TLS / sessions / tokens can be
// trusted — failing loudly is the only safe response.
func randomDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure in randomDeviceID: %v", err))
	}
	return hex.EncodeToString(b)
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
	db.RecordEvent(&db.Event{
		Severity: authEventSeverity(event),
		Source:   "auth",
		Kind:     event,
		Actor:    "user",
		IP:       ip,
		Message:  event, // event names are already human-readable enough; a richer template can come later
	})
	log.Printf("gopher-auth: %s ip=%s", event, ip)
}

// LogAuditEvent records an audit-log entry from outside this package (e.g.
// handlers tagging successful sensitive ops like SSH key downloads).
func (s *AuthService) LogAuditEvent(event, ip string) {
	s.logEvent(event, ip)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

