package api

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/cors"
	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

func CORSMiddleware() func(http.Handler) http.Handler {
return cors.Handler(cors.Options{
AllowedOrigins:   []string{"*"},
AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
ExposedHeaders:   []string{"Link"},
AllowCredentials: false,
MaxAge:           300,
})
}

func LoggingMiddleware(next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
start := time.Now()
rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
next.ServeHTTP(rw, r)
log.Printf("%s %s %d %v", r.Method, r.URL.Path, rw.status, time.Since(start))
})
}

func RecoveryMiddleware(next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
defer func() {
if rvr := recover(); rvr != nil {
log.Printf("panic: %v\n%s", rvr, debug.Stack())
response.InternalError(w, "internal server error")
}
}()
next.ServeHTTP(w, r)
})
}

func AuthMiddleware(authSvc *service.AuthService) func(http.Handler) http.Handler {
return func(next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
cookie, err := r.Cookie("gopher_session")
if err != nil || !authSvc.ValidateSession(cookie.Value) {
response.Error(w, http.StatusUnauthorized, "unauthorized")
return
}
next.ServeHTTP(w, r)
})
}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

// Hijack forwards the hijack call to the underlying ResponseWriter so that
// WebSocket upgrades (which require net.Conn access) work through this wrapper.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// Flush forwards flush to the underlying ResponseWriter for SSE / streaming.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
