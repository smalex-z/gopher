package api

import (
"log"
"net/http"
"runtime/debug"
"time"

"github.com/go-chi/cors"
"github.com/smalex-z/gopher/internal/api/response"
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

type responseWriter struct {
http.ResponseWriter
status int
}

func (rw *responseWriter) WriteHeader(status int) {
rw.status = status
rw.ResponseWriter.WriteHeader(status)
}
