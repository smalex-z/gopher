package db

import (
"fmt"

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

func NextRatholePort() (int, error) {
	var tunnel Tunnel
	if err := DB.Order("rathole_port DESC").First(&tunnel).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return 20000, nil
		}
		return 0, err
	}
	if tunnel.RatholePort < 20000 {
		return 20000, nil
	}
	return tunnel.RatholePort + 1, nil
}

func CheckSubdomainExists(subdomain string) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("subdomain = ?", subdomain).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func CheckRatholePortExists(port int) (bool, error) {
	var count int64
	if err := DB.Model(&Tunnel{}).Where("rathole_port = ?", port).Count(&count).Error; err != nil {
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

func NextSSHTunnelPort() (int, error) {
var m Machine
if err := DB.Order("tunnel_port DESC").First(&m).Error; err != nil {
if err == gorm.ErrRecordNotFound {
return 10000, nil
}
return 0, err
}
if m.TunnelPort == 0 {
return 10000, nil
}
return m.TunnelPort + 1, nil
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
