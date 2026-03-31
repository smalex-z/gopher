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
	updateSvc *service.UpdateService,
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
	debugH := handlers.NewDebugHandler()
	updateH := handlers.NewUpdateHandler(updateSvc)

	// Public: bootstrap script download and machine self-registration
	r.Get("/static/bootstrap.sh", bootstrapH.ServeScript)
	r.Get("/static/gopher-uninstall.sh", bootstrapH.ServeUninstallScript)
	r.Post("/api/bootstrap", bootstrapH.Register)

	r.Route("/api", func(r chi.Router) {
		// Public auth + health routes
		r.Get("/status", handlers.StatusHandler)
		r.Get("/auth/status", authH.Status)
		r.Post("/auth/setup", authH.Setup)
		r.Post("/auth/login", authH.Login)
		r.Get("/local/status", localH.Status)
		r.Post("/local/install", localH.Install)
		r.Post("/local/skip", localH.Skip)
		r.Get("/local/logs/ws", logsH.WebSocketDuringSetup)
		r.Get("/local/check-dns", localH.CheckDNS)

		// All routes below require a valid session
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(authSvc))

			r.Post("/auth/logout", authH.Logout)

			r.Post("/bootstrap/token", bootstrapH.GenerateToken)

			r.Route("/local", func(r chi.Router) {
				r.Post("/reconcile", localH.Reconcile)
				r.Route("/ssh-keys", func(r chi.Router) {
					r.Get("/", localH.ListSSHKeys)
					r.Post("/generate", localH.GenerateSSHKey)
					r.Post("/upload", localH.UploadSSHKey)
					r.Delete("/{id}", localH.DeleteSSHKey)
					r.Put("/{id}/default", localH.SetDefaultSSHKey)
					r.Get("/{id}/download", localH.DownloadSSHKey)
				})
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
				r.Put("/{id}/ssh-key", machineH.ReassignSSHKey)
			})

			r.Route("/tunnels", func(r chi.Router) {
				r.Get("/", tunnelH.List)
				r.Get("/next-port", tunnelH.NextPort)
				r.Post("/", tunnelH.Create)
				r.Get("/{id}", tunnelH.Get)
				r.Put("/{id}", tunnelH.Update)
				r.Delete("/{id}", tunnelH.Delete)
				r.Post("/{id}/test", tunnelH.Test)
			})

			r.Route("/logs", func(r chi.Router) {
				r.Get("/ws", logsH.WebSocket)
			})

			r.Route("/debug", func(r chi.Router) {
				r.Get("/caddyfile", debugH.GetCaddyfile)
				r.Get("/rathole-server", debugH.GetRatholeServerConfig)
			})

			r.Route("/update", func(r chi.Router) {
				r.Get("/check", updateH.Check)
				r.Post("/apply", updateH.Apply)
			})
		})
	})

	return r
}
