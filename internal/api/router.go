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
	bootstrapSvc *service.BootstrapService,
	authSvc *service.AuthService,
	localSvc *service.LocalSetupService,
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
	bootstrapH := handlers.NewBootstrapHandler(bootstrapSvc)
	authH := handlers.NewAuthHandler(authSvc)
	localH := handlers.NewLocalHandler(localSvc)

	// Public: bootstrap script download and machine self-registration
	r.Get("/static/bootstrap.sh", bootstrapH.ServeScript)
	r.Post("/api/bootstrap", bootstrapH.Register)

	r.Route("/api", func(r chi.Router) {
		// Public auth + health routes
		r.Get("/status", handlers.StatusHandler)
		r.Get("/auth/status", authH.Status)
		r.Post("/auth/setup", authH.Setup)
		r.Post("/auth/login", authH.Login)

		// All routes below require a valid session
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(authSvc))

			r.Post("/auth/logout", authH.Logout)

			r.Post("/bootstrap/token", bootstrapH.GenerateToken)

			r.Route("/local", func(r chi.Router) {
				r.Get("/status", localH.Status)
				r.Post("/install", localH.Install)
				r.Post("/skip", localH.Skip)
			})

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
	})

	return r
}
