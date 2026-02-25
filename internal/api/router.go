package api

import (
"net/http"

"github.com/go-chi/chi/v5"
chimiddleware "github.com/go-chi/chi/v5/middleware"
"github.com/smalex-z/gopher/internal/api/handlers"
"github.com/smalex-z/gopher/internal/service"
)

func NewRouter(
vpsSvc *service.VPSService,
machineSvc *service.MachineService,
tunnelSvc *service.TunnelService,
deploySvc *service.DeployService,
) http.Handler {
r := chi.NewRouter()

r.Use(CORSMiddleware())
r.Use(chimiddleware.RequestID)
r.Use(LoggingMiddleware)
r.Use(RecoveryMiddleware)

vpsH := handlers.NewVPSHandler(vpsSvc)
machineH := handlers.NewMachineHandler(machineSvc)
tunnelH := handlers.NewTunnelHandler(tunnelSvc)
logsH := handlers.NewLogsHandler(deploySvc.Hub)

r.Route("/api", func(r chi.Router) {
r.Get("/status", handlers.StatusHandler)

r.Route("/vps", func(r chi.Router) {
r.Get("/", vpsH.Get)
r.Post("/setup", vpsH.Create)
r.Put("/", vpsH.Update)
r.Delete("/", vpsH.Delete)
r.Post("/bootstrap", vpsH.Bootstrap)
r.Post("/deploy", vpsH.Deploy)
r.Get("/status", vpsH.Status)
})

r.Route("/machines", func(r chi.Router) {
r.Get("/", machineH.List)
r.Post("/", machineH.Create)
r.Get("/{id}", machineH.Get)
r.Put("/{id}", machineH.Update)
r.Delete("/{id}", machineH.Delete)
r.Post("/{id}/deploy", machineH.Deploy)
r.Get("/{id}/status", machineH.Status)
})

r.Route("/tunnels", func(r chi.Router) {
r.Get("/", tunnelH.List)
r.Post("/", tunnelH.Create)
r.Get("/{id}", tunnelH.Get)
r.Put("/{id}", tunnelH.Update)
r.Delete("/{id}", tunnelH.Delete)
r.Post("/{id}/test", tunnelH.Test)
})

r.Route("/logs", func(r chi.Router) {
r.Get("/ws", logsH.WebSocket)
})
})

return r
}
