package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pquerna/otp/totp"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const (
	totpIssuer       = "Gopher"
	backupCodeCount  = 10
	backupCodeBytes  = 5 // 10 hex chars per code → "XXXXX-XXXXX"
)

// generateBackupCodes generates backupCodeCount one-time codes.
// Returns (plaintext codes for display, bcrypt-hashed codes for storage).
func generateBackupCodes() ([]string, []string, error) {
	plain := make([]string, backupCodeCount)
	hashed := make([]string, backupCodeCount)
	buf := make([]byte, backupCodeBytes)
	for i := range plain {
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, fmt.Errorf("failed to generate backup code: %w", err)
		}
		code := fmt.Sprintf("%X", buf)
		plain[i] = code[:5] + "-" + code[5:]
		h, err := bcrypt.GenerateFromPassword([]byte(strings.ReplaceAll(plain[i], "-", "")), bcrypt.MinCost)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to hash backup code: %w", err)
		}
		hashed[i] = string(h)
	}
	return plain, hashed, nil
}

// marshalBackupCodes encodes hashed backup codes as JSON for DB storage.
func marshalBackupCodes(hashed []string) (string, error) {
	b, err := json.Marshal(hashed)
	return string(b), err
}

// unmarshalBackupCodes decodes backup codes from DB storage.
func unmarshalBackupCodes(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var codes []string
	if err := json.Unmarshal([]byte(raw), &codes); err != nil {
		return nil, err
	}
	return codes, nil
}

// generateTOTPSecret creates a new TOTP key for the given account and returns
// the base32 secret and a QR code PNG as a base64 data URL.
func generateTOTPSecret(accountName string) (secret, qrDataURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to generate TOTP key: %w", err)
	}

	png, err := qrcode.Encode(key.URL(), qrcode.Medium, 256)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate QR code: %w", err)
	}

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	return key.Secret(), dataURL, nil
}

// verifyTOTP checks a 6-digit TOTP code against the given base32 secret.
func verifyTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

// verifyAndConsumeBackupCode checks a code against the stored hashed codes.
// If matched, it removes that code and returns the updated JSON for storage.
// normalizeCode strips dashes so users can enter codes either way.
func verifyAndConsumeBackupCode(raw, code string) (matched bool, updatedJSON string, err error) {
	codes, err := unmarshalBackupCodes(raw)
	if err != nil {
		return false, raw, err
	}
	normalized := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	for i, h := range codes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(normalized)) == nil {
			codes = append(codes[:i], codes[i+1:]...)
			updated, merr := marshalBackupCodes(codes)
			if merr != nil {
				return false, raw, merr
			}
			return true, updated, nil
		}
	}
	return false, raw, nil
}
