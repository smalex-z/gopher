package dto

type CreateTunnelRequest struct {
MachineID string `json:"machine_id"`
Name      string `json:"name"`
Subdomain string `json:"subdomain"`
LocalPort int    `json:"local_port"`
Protocol  string `json:"protocol"`
}

type UpdateTunnelRequest struct {
Name      string `json:"name"`
Subdomain string `json:"subdomain"`
LocalPort int    `json:"local_port"`
Protocol  string `json:"protocol"`
}
