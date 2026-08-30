package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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
//
// Sessions are persisted (hashed) in the DB, NOT in memory: gopher restarts
// itself during normal operation — the post-install supervisor kick and
// self-updates — and in-memory sessions logged the operator out mid-setup
// (step 3 of the wizard 401'd because step 2's install restarted the service).
// Only the short-lived pending-TOTP tokens stay in memory; losing one across
// a restart just means re-entering the password.

type pendingTOTPEntry struct {
	expiresAt time.Time
}

// hashSessionToken derives the DB key from a bearer token. Sessions are
// stored hashed so a leaked/backed-up DB doesn't yield usable tokens.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LoginResult is returned by Login.
type LoginResult struct {
	Token        string // session token (empty if NeedsTOTP)
	NeedsTOTP    bool
	PendingToken string // short-lived proof-of-password for the TOTP step
}

type AuthService struct {
	mu          sync.RWMutex
	pendingTOTP map[string]pendingTOTPEntry
	rl          *loginRateLimiter
}

func NewAuthService() *AuthService {
	return &AuthService{
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
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	return db.MutateSettings(func(settings *db.AppSettings) error {
		if settings.IsSetup {
			return fmt.Errorf("already configured")
		}
		settings.PasswordHash = string(hash)
		settings.IsSetup = true
		return nil
	})
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
// Rate-limited per-IP for defense in depth. The pendingToken is consumed only
// on a SUCCESSFUL code (or once it expires) — a wrong guess leaves it usable so
// the operator can retry within its 5-minute window. Brute force is bounded by
// this per-IP rate limiter plus the expiry, not by burning the challenge on the
// first wrong code (which used to make a single typo cascade into "2FA expired"
// on every subsequent attempt).
func (s *AuthService) LoginTOTP(pendingToken, code, ip string) (string, error) {
	if !s.rl.record(ip) {
		s.logEvent("LOGIN_TOTP_RATE_LIMITED", ip)
		return "", fmt.Errorf("too many attempts")
	}

	// Look up the pending-2FA challenge but DO NOT consume it on a failed
	// attempt — only on success (see consume() below). Deleting it on every
	// attempt meant a single mistyped code burned the whole challenge, and
	// every subsequent code the operator entered (correct or not) then failed
	// as "2FA expired" until they went back and re-entered their password.
	// Backup codes especially read as "they all came up expired" after one
	// fat-finger. Brute force is already bounded by the per-IP rate limiter
	// above and the 5-minute expiry — single-use-on-failure adds no security,
	// only a footgun.
	s.mu.Lock()
	entry, ok := s.pendingTOTP[pendingToken]
	if ok && time.Now().After(entry.expiresAt) {
		delete(s.pendingTOTP, pendingToken) // genuinely expired — clean it up
		ok = false
	}
	s.mu.Unlock()

	if !ok {
		s.logEvent("LOGIN_FAILED_2FA_EXPIRED", ip)
		return "", fmt.Errorf("invalid or expired token")
	}

	// consume invalidates the challenge; call it only on a successful login so
	// a wrong guess leaves the challenge usable for a retry within its window.
	consume := func() {
		s.mu.Lock()
		delete(s.pendingTOTP, pendingToken)
		s.mu.Unlock()
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
		consume()
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
		if saveErr := db.MutateSettings(func(s *db.AppSettings) error {
			s.TOTPBackupCodes = updatedCodes
			return nil
		}); saveErr != nil {
			log.Printf("WARN: failed to save consumed backup code: %v", saveErr)
		}
		consume()
		token, err := s.createSession()
		if err != nil {
			return "", err
		}
		s.rl.Reset(ip)
		s.logEvent("LOGIN_SUCCESS_BACKUP_CODE", ip)
		return token, nil
	}

	// Wrong code: leave the challenge intact so the operator can retry within
	// the window (rate-limited). No consume() here — that was the bug.
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
			if err := db.MutateSettings(func(s *db.AppSettings) error {
				s.TOTPBackupCodes = updatedBackup
				return nil
			}); err != nil {
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
	_ = db.DeleteDashboardSession(hashSessionToken(token))
}

func (s *AuthService) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	hash := hashSessionToken(token)
	sess, err := db.GetDashboardSession(hash)
	if err != nil || sess == nil {
		return false
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = db.DeleteDashboardSession(hash)
		return false
	}
	// Sliding expiry, but only write when at least an hour has been consumed —
	// the dashboard polls every 15s and a DB write per request is pointless.
	if remaining := time.Until(sess.ExpiresAt); remaining < sessionDuration-time.Hour {
		_ = db.TouchDashboardSession(hash, time.Now().Add(sessionDuration))
	}
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

// verifyTOTPAgainstDevices is the pure core: walk a device slice, return the
// matching device ID on the first hit. Takes the devices explicitly so it can
// be called with rows already read via a transaction (inside MutateSettingsTx)
// OR via the global pool — the caller decides where the read happens, which
// matters because a global-pool read inside a settings transaction deadlocks.
func verifyTOTPAgainstDevices(devices []db.TOTPDevice, code string) (string, bool) {
	for _, d := range devices {
		if verifyTOTP(d.Secret, code) {
			return d.ID, true
		}
	}
	return "", false
}

// verifyCodeOrBackup matches against the supplied devices first, then backup
// codes. Returns (deviceID, backupConsumedJSON, ok); a matched backup code
// yields updated JSON the caller must persist.
func verifyCodeOrBackup(devices []db.TOTPDevice, code, backupCodesJSON string) (deviceID, updatedBackupJSON string, ok bool) {
	if id, hit := verifyTOTPAgainstDevices(devices, code); hit {
		return id, backupCodesJSON, true
	}
	matched, updated, err := verifyAndConsumeBackupCode(backupCodesJSON, code)
	if err != nil || !matched {
		return "", backupCodesJSON, false
	}
	return "", updated, true
}

// verifyTOTPAcrossDevices / verifyTOTPOrBackup are the global-pool convenience
// wrappers, safe to call OUTSIDE any transaction (e.g. LoginTOTP). They MUST
// NOT be used inside a MutateSettingsTx closure — read devices via
// db.GetTOTPDevicesTx(tx) there and call the pure helpers above instead.
func verifyTOTPAcrossDevices(code string) (string, bool) {
	devices, err := db.GetTOTPDevices()
	if err != nil {
		return "", false
	}
	return verifyTOTPAgainstDevices(devices, code)
}

func verifyTOTPOrBackup(code, backupCodesJSON string) (deviceID, updatedBackupJSON string, ok bool) {
	devices, err := db.GetTOTPDevices()
	if err != nil {
		return "", backupCodesJSON, false
	}
	return verifyCodeOrBackup(devices, code, backupCodesJSON)
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
	if err := db.MutateSettings(func(settings *db.AppSettings) error {
		settings.TOTPSecret = secret
		return nil
	}); err != nil {
		return "", "", fmt.Errorf("failed to save pending TOTP secret: %w", err)
	}
	return secret, qrDataURL, nil
}

// TOTPConfirm finalises an enrollment: verifies the code against the pending
// secret, persists a new TOTPDevice with the given name, clears the pending
// slot, and (only if this is the first device) generates backup codes.
// Returns plaintext backup codes only on first enrollment; nil otherwise.
//
// Rate-limited per-IP (shared login bucket): every 2FA-management endpoint
// that verifies a 6-digit code is a brute-force target for an attacker
// holding a stolen session cookie — unthrottled, ~1M guesses walks it.
func (s *AuthService) TOTPConfirm(code, name, ip string) ([]string, error) {
	if !s.rl.record(ip) {
		s.logEvent("TOTP_RATE_LIMITED", ip)
		return nil, fmt.Errorf("too many attempts")
	}
	deviceName := strings.TrimSpace(name)
	if deviceName == "" {
		deviceName = "Authenticator"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}

	var plain []string
	if err := db.MutateSettingsTx(func(tx *gorm.DB, settings *db.AppSettings) error {
		if settings.TOTPSecret == "" {
			return fmt.Errorf("no enrollment in progress; call enroll first")
		}
		if !verifyTOTP(settings.TOTPSecret, code) {
			return fmt.Errorf("invalid code")
		}
		device := &db.TOTPDevice{
			ID:        randomDeviceID(),
			Name:      deviceName,
			Secret:    settings.TOTPSecret,
			CreatedAt: time.Now(),
		}
		// Tx-scoped write: db.CreateTOTPDevice (global pool) here was the exact
		// call that self-deadlocked the server — see MutateSettingsTx.
		if err := db.CreateTOTPDeviceTx(tx, device); err != nil {
			return fmt.Errorf("failed to save device: %w", err)
		}

		// Clear the pending slot.
		settings.TOTPSecret = ""

		// Generate backup codes only if this is the first device. Otherwise keep
		// the existing set; backup codes are shared across devices.
		count, err := db.CountTOTPDevicesTx(tx)
		if err != nil {
			return err
		}
		if count == 1 || settings.TOTPBackupCodes == "" {
			var hashed []string
			generated, h, err := generateBackupCodes()
			if err != nil {
				return err
			}
			plain = generated
			hashed = h
			codesJSON, err := marshalBackupCodes(hashed)
			if err != nil {
				return err
			}
			settings.TOTPBackupCodes = codesJSON
		}
		settings.TOTPEnabled = true
		return nil
	}); err != nil {
		return nil, err
	}
	return plain, nil
}

// TOTPDisable removes ALL devices and clears backup codes. Requires a valid
// code from any device or a backup code. Rate-limited per-IP — see
// TOTPConfirm.
func (s *AuthService) TOTPDisable(code, ip string) error {
	if !s.rl.record(ip) {
		s.logEvent("TOTP_RATE_LIMITED", ip)
		return fmt.Errorf("too many attempts")
	}
	return db.MutateSettingsTx(func(tx *gorm.DB, settings *db.AppSettings) error {
		if !settings.TOTPEnabled {
			return fmt.Errorf("2FA is not enabled")
		}
		devices, err := db.GetTOTPDevicesTx(tx)
		if err != nil {
			return err
		}
		_, _, ok := verifyCodeOrBackup(devices, code, settings.TOTPBackupCodes)
		if !ok {
			return fmt.Errorf("invalid code")
		}
		if err := db.DeleteAllTOTPDevicesTx(tx); err != nil {
			return fmt.Errorf("failed to delete devices: %w", err)
		}
		settings.TOTPEnabled = false
		settings.TOTPSecret = ""
		settings.TOTPBackupCodes = ""
		return nil
	})
}

// TOTPRemoveDevice removes a single device. Requires a valid code from any
// enrolled device (including the one being removed — the code authenticates
// the action, not the device) or a backup code. If this leaves zero devices,
// 2FA is disabled and backup codes are cleared. Rate-limited per-IP — see
// TOTPConfirm.
func (s *AuthService) TOTPRemoveDevice(deviceID, code, ip string) error {
	if !s.rl.record(ip) {
		s.logEvent("TOTP_RATE_LIMITED", ip)
		return fmt.Errorf("too many attempts")
	}
	return db.MutateSettingsTx(func(tx *gorm.DB, settings *db.AppSettings) error {
		if !settings.TOTPEnabled {
			return fmt.Errorf("2FA is not enabled")
		}
		if _, err := db.GetTOTPDeviceTx(tx, deviceID); err != nil {
			return err
		}
		devices, err := db.GetTOTPDevicesTx(tx)
		if err != nil {
			return err
		}
		_, updatedBackup, ok := verifyCodeOrBackup(devices, code, settings.TOTPBackupCodes)
		if !ok {
			return fmt.Errorf("invalid code")
		}
		settings.TOTPBackupCodes = updatedBackup
		if err := db.DeleteTOTPDeviceTx(tx, deviceID); err != nil {
			return fmt.Errorf("failed to delete device: %w", err)
		}
		count, err := db.CountTOTPDevicesTx(tx)
		if err != nil {
			return err
		}
		if count == 0 {
			// Last device removed — disable 2FA entirely.
			settings.TOTPEnabled = false
			settings.TOTPSecret = ""
			settings.TOTPBackupCodes = ""
		}
		return nil
	})
}

// TOTPRegenerateBackupCodes mints a fresh backup-code set after verifying a
// code. Rate-limited per-IP — see TOTPConfirm.
func (s *AuthService) TOTPRegenerateBackupCodes(code, ip string) ([]string, error) {
	if !s.rl.record(ip) {
		s.logEvent("TOTP_RATE_LIMITED", ip)
		return nil, fmt.Errorf("too many attempts")
	}
	plain, hashed, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	codesJSON, err := marshalBackupCodes(hashed)
	if err != nil {
		return nil, err
	}
	if err := db.MutateSettingsTx(func(tx *gorm.DB, settings *db.AppSettings) error {
		if !settings.TOTPEnabled {
			return fmt.Errorf("2FA is not enabled")
		}
		devices, err := db.GetTOTPDevicesTx(tx)
		if err != nil {
			return err
		}
		if _, ok := verifyTOTPAgainstDevices(devices, code); !ok {
			return fmt.Errorf("invalid code")
		}
		settings.TOTPBackupCodes = codesJSON
		return nil
	}); err != nil {
		return nil, err
	}
	return plain, nil
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
	if err := db.CreateDashboardSession(hashSessionToken(token), time.Now().Add(sessionDuration)); err != nil {
		return "", fmt.Errorf("failed to persist session: %w", err)
	}
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
