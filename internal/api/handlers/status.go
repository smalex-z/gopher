package handlers

import (
"net/http"

"github.com/smalex-z/gopher/internal/api/response"
"github.com/smalex-z/gopher/internal/db"
)

func StatusHandler(w http.ResponseWriter, r *http.Request) {
machines, _ := db.GetMachines()
tunnels, _ := db.GetTunnels()
vps, _ := db.GetVPS()

data := map[string]interface{}{
"machines": len(machines),
"tunnels":  len(tunnels),
"vps":      vps != nil,
}
response.Success(w, data)
}
