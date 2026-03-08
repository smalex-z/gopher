package dto

type CreateVPSRequest struct {
Host       string `json:"host"`
Port       int    `json:"port"`
Username   string `json:"username"`
PrivateKey string `json:"private_key"`
Domain     string `json:"domain"`
}

type UpdateVPSRequest struct {
Host       string `json:"host"`
Port       int    `json:"port"`
Username   string `json:"username"`
PrivateKey string `json:"private_key"`
Domain     string `json:"domain"`
}
