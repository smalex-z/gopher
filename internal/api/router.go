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
	secSvc *service.SecurityService,
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
	firewallH := handlers.NewFirewallHandler(localSvc)
	securityH := handlers.NewSecurityHandler(authSvc, secSvc)
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
		r.Post("/auth/login/2fa", authH.LoginTOTP)
		r.Get("/local/status", localH.Status)
		r.Post("/local/install", localH.Install)
		r.Post("/local/skip", localH.Skip)
		r.Get("/local/logs/ws", logsH.WebSocketDuringSetup)
		r.Get("/local/check-dns", localH.CheckDNS)
		r.Get("/local/resolve-ip", localH.ResolveIP)
		r.Get("/local/detect-ip", localH.DetectIP)
		r.Get("/local/firewall/detect", localH.DetectFirewall)
		r.Post("/local/firewall/configure", localH.ConfigureFirewall)

		// All routes below require a valid session
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(authSvc))

			r.Post("/auth/logout", authH.Logout)

			r.Route("/security", func(r chi.Router) {
				r.Get("/logs", securityH.AuditLog)
				r.Get("/fail2ban", securityH.Fail2banStatus)
				r.Post("/fail2ban/unban", securityH.UnbanIP)
				r.Get("/fail2ban/config", securityH.GetFail2banConfig)
				r.Put("/fail2ban/config", securityH.SaveFail2banConfig)
				r.Post("/fail2ban/whitelist", securityH.AddWhitelistIP)
				r.Delete("/fail2ban/whitelist/{ip}", securityH.RemoveWhitelistIP)
			})

			r.Route("/auth/2fa", func(r chi.Router) {
				r.Get("/status", authH.TOTPStatus)
				r.Post("/enroll", authH.TOTPEnroll)
				r.Post("/confirm", authH.TOTPConfirm)
				r.Post("/disable", authH.TOTPDisable)
				r.Post("/backup-codes/regenerate", authH.TOTPRegenerateBackupCodes)
			})

			r.Post("/bootstrap/token", bootstrapH.GenerateToken)

			r.Route("/local", func(r chi.Router) {
				r.Post("/reconcile", localH.Reconcile)
				r.Post("/setup-fail2ban", localH.SetupFail2ban)
				r.Put("/server-ports", localH.SetServerPorts)
				r.Route("/firewall", func(r chi.Router) {
					r.Get("/overview", firewallH.Overview)
					r.Post("/rules", firewallH.CreateRule)
					r.Delete("/rules/{id}", firewallH.DeleteRule)
					r.Get("/live", firewallH.LiveRules)
					r.Post("/reload", firewallH.Reload)
				})
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
				r.Get("/{id}/network-info", machineH.NetworkInfo)
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
