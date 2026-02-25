package dto

type CreateMachineRequest struct {
Name       string `json:"name"`
Host       string `json:"host"`
Port       int    `json:"port"`
Username   string `json:"username"`
PrivateKey string `json:"private_key"`
}

type UpdateMachineRequest struct {
Name       string `json:"name"`
Host       string `json:"host"`
Port       int    `json:"port"`
Username   string `json:"username"`
PrivateKey string `json:"private_key"`
}
