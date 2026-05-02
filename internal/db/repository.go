package db

import (
	"fmt"
	"time"

	apperrors "github.com/smalex-z/gopher/internal/errors"
	"gorm.io/gorm"
)

// VPS Repository

func GetVPS() (*VPSConfig, error) {
	var vps VPSConfig
	if err := DB.First(&vps).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "vps_config", ID: "singleton"}
		}
		return nil, err
	}
	return &vps, nil
}

func CreateVPS(vps *VPSConfig) error {
	return DB.Create(vps).Error
}

func UpdateVPS(vps *VPSConfig) error {
	return DB.Save(vps).Error
}

func DeleteVPS(id string) error {
	return DB.Delete(&VPSConfig{}, "id = ?", id).Error
}

// Machine Repository

func GetMachines() ([]Machine, error) {
	var machines []Machine
	if err := DB.Preload("Tunnels").Find(&machines).Error; err != nil {
		return nil, err
	}
	return machines, nil
}

func GetMachine(id string) (*Machine, error) {
	var machine Machine
	if err := DB.Preload("Tunnels").First(&machine, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "machine", ID: id}
		}
		return nil, err
	}
	return &machine, nil
}

func CreateMachine(machine *Machine) error {
	return DB.Create(machine).Error
}

func UpdateMachine(machine *Machine) error {
	return DB.Save(machine).Error
}

func DeleteMachine(id string) error {
	return DB.Delete(&Machine{}, "id = ?", id).Error
}

// Tunnel Repository

func GetTunnels() ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Find(&tunnels).Error; err != nil {
		return nil, err
	}
	return tunnels, nil
}

func GetTunnelsByMachine(machineID string) ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Where("machine_id = ?", machineID).Find(&tunnels).Error; err != nil {
		return nil, err
	}
	return tunnels, nil
}

func GetTunnel(id string) (*Tunnel, error) {
	var tunnel Tunnel
	if err := DB.First(&tunnel, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "tunnel", ID: id}
		}
		return nil, err
	}
	return &tunnel, nil
}

func CreateTunnel(tunnel *Tunnel) error {
	return DB.Create(tunnel).Error
}

func UpdateTunnel(tunnel *Tunnel) error {
	return DB.Save(tunnel).Error
}

func DeleteTunnel(id string) error {
	return DB.Delete(&Tunnel{}, "id = ?", id).Error
}

// allUsedPorts returns every port currently assigned across both service tunnels
// and machine SSH tunnels. Used by port-assignment and conflict-check functions
// so neither table is blind to the other.
func allUsedPorts() (map[int]bool, error) {
	used := make(map[int]bool)
	var tunnels []Tunnel
	if err := DB.Select("rathole_port").Find(&tunnels).Error; err != nil {
		return nil, err
	}
	for _, t := range tunnels {
		if t.RatholePort > 0 {
			used[t.RatholePort] = true
		}
	}
	var machines []Machine
	if err := DB.Select("tunnel_port").Find(&machines).Error; err != nil {
		return nil, err
	}
	for _, m := range machines {
		if m.TunnelPort > 0 {
			used[m.TunnelPort] = true
		}
	}
	return used, nil
}

// NextRatholePort returns the next available port for a service tunnel,
// guaranteed free across both service tunnels and machine SSH tunnels.
// Starts from 1024 (first non-privileged port) and finds the first gap.
func NextRatholePort() (int, error) {
	used, err := allUsedPorts()
	if err != nil {
		return 0, err
	}
	port := 1024
	for used[port] {
		port++
	}
	return port, nil
}

func CheckSubdomainExists(subdomain string) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("subdomain = ?", subdomain).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CheckRatholePortExists returns true if port is already in use by any service
// tunnel or machine SSH tunnel.
func CheckRatholePortExists(port int) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("rathole_port = ?", port).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := DB.Model(&Machine{}).Where("tunnel_port = ?", port).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetAllTunnelsForVPS() ([]Tunnel, error) {
	var tunnels []Tunnel
	if err := DB.Find(&tunnels).Error; err != nil {
		return nil, fmt.Errorf("failed to get tunnels: %w", err)
	}
	return tunnels, nil
}

// SSH Key Repository

func GetSSHKeys() ([]SSHKey, error) {
	var keys []SSHKey
	if err := DB.Order("created_at ASC").Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func GetSSHKey(id string) (*SSHKey, error) {
	var key SSHKey
	if err := DB.First(&key, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "ssh_key", ID: id}
		}
		return nil, err
	}
	return &key, nil
}

func GetDefaultSSHKey() (*SSHKey, error) {
	var key SSHKey
	if err := DB.Where("is_default = ?", true).First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "ssh_key", ID: "default"}
		}
		return nil, err
	}
	return &key, nil
}

func CreateSSHKey(key *SSHKey) error {
	return DB.Create(key).Error
}

func UpdateSSHKey(key *SSHKey) error {
	return DB.Save(key).Error
}

func DeleteSSHKeyByID(id string) error {
	return DB.Delete(&SSHKey{}, "id = ?", id).Error
}

func SetDefaultSSHKey(id string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&SSHKey{}).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&SSHKey{}).Where("id = ?", id).Update("is_default", true).Error
	})
}

func CountSSHKeys() (int64, error) {
	var count int64
	return count, DB.Model(&SSHKey{}).Count(&count).Error
}

func CountMachinesUsingKey(keyID string) (int64, error) {
	var count int64
	return count, DB.Model(&Machine{}).Where("ssh_key_id = ?", keyID).Count(&count).Error
}

func GetSSHKeyForMachine(machine *Machine) (*SSHKey, error) {
	if machine.SSHKeyID != "" {
		key, err := GetSSHKey(machine.SSHKeyID)
		if err == nil {
			return key, nil
		}
	}
	return GetDefaultSSHKey()
}

// Bootstrap Token Repository

func CreateBootstrapToken(t *BootstrapToken) error {
	return DB.Create(t).Error
}

func GetBootstrapToken(token string) (*BootstrapToken, error) {
	var bt BootstrapToken
	if err := DB.Where("token = ?", token).First(&bt).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "bootstrap_token", ID: token}
		}
		return nil, err
	}
	return &bt, nil
}

func MarkTokenUsed(tokenID, machineID string) error {
	return DB.Model(&BootstrapToken{}).Where("id = ?", tokenID).Updates(map[string]interface{}{
		"used_at":    DB.NowFunc(),
		"machine_id": machineID,
	}).Error
}

// NextSSHTunnelPort returns the next available port for a machine SSH tunnel,
// guaranteed free across both machine SSH tunnels and service tunnels.
// Starts from 1024 (first non-privileged port) and finds the first gap.
func NextSSHTunnelPort() (int, error) {
	used, err := allUsedPorts()
	if err != nil {
		return 0, err
	}
	port := 1024
	for used[port] {
		port++
	}
	return port, nil
}

// App Settings Repository

func GetSettings() (*AppSettings, error) {
	var s AppSettings
	if err := DB.First(&s, "id = 'singleton'").Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return &AppSettings{ID: "singleton", IsSetup: false}, nil
		}
		return nil, err
	}
	return &s, nil
}

func SaveSettings(s *AppSettings) error {
	s.ID = "singleton"
	return DB.Save(s).Error
}

// Firewall Rules Repository

func GetFirewallRules() ([]FirewallRule, error) {
	var rules []FirewallRule
	if err := DB.Order("created_at ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func GetFirewallRule(id string) (*FirewallRule, error) {
	var rule FirewallRule
	if err := DB.First(&rule, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "firewall_rule", ID: id}
		}
		return nil, err
	}
	return &rule, nil
}

func CreateFirewallRule(rule *FirewallRule) error {
	return DB.Create(rule).Error
}

func DeleteFirewallRule(id string) error {
	return DB.Delete(&FirewallRule{}, "id = ?", id).Error
}

// Bot Session Repository

// GetTunnelBySubdomain returns the tunnel with the given subdomain value.
func GetTunnelBySubdomain(subdomain string) (*Tunnel, error) {
	var t Tunnel
	if err := DB.Where("subdomain = ?", subdomain).First(&t).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "tunnel", ID: subdomain}
		}
		return nil, err
	}
	return &t, nil
}

// CreateBotSession persists a newly issued bot challenge session.
func CreateBotSession(s *BotSession) error {
	return DB.Create(s).Error
}

// PurgeBotSessions deletes all expired bot sessions.
func PurgeBotSessions() error {
	return DB.Where("expires_at < ?", gorm.Expr("datetime('now')")).Delete(&BotSession{}).Error
}

// ── TOTP Devices ─────────────────────────────────────────────────────────────

func GetTOTPDevices() ([]TOTPDevice, error) {
	var devices []TOTPDevice
	if err := DB.Order("created_at ASC").Find(&devices).Error; err != nil {
		return nil, err
	}
	return devices, nil
}

func GetTOTPDevice(id string) (*TOTPDevice, error) {
	var d TOTPDevice
	if err := DB.First(&d, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, &apperrors.NotFoundError{Resource: "totp_device", ID: id}
		}
		return nil, err
	}
	return &d, nil
}

func CreateTOTPDevice(d *TOTPDevice) error {
	return DB.Create(d).Error
}

func DeleteTOTPDevice(id string) error {
	return DB.Delete(&TOTPDevice{}, "id = ?", id).Error
}

func DeleteAllTOTPDevices() error {
	return DB.Exec("DELETE FROM totp_devices").Error
}

func CountTOTPDevices() (int64, error) {
	var count int64
	return count, DB.Model(&TOTPDevice{}).Count(&count).Error
}

func TouchTOTPDevice(id string) error {
	now := time.Now()
	return DB.Model(&TOTPDevice{}).Where("id = ?", id).Update("last_used_at", &now).Error
}
