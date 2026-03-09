package dto

type CreateTunnelRequest struct {
	MachineID   string `json:"machine_id"`
	Name        string `json:"name"`
	Subdomain   string `json:"subdomain"`
	LocalPort   int    `json:"local_port"`
	RatholePort int    `json:"rathole_port"` // 0 = auto-assign
}

type UpdateTunnelRequest struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
	LocalPort int    `json:"local_port"`
}
