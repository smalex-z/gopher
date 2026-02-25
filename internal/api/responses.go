package api

import (
"net/http"

"github.com/smalex-z/gopher/internal/api/response"
)

func Success(w http.ResponseWriter, data interface{}) {
response.Success(w, data)
}

func Created(w http.ResponseWriter, data interface{}) {
response.Created(w, data)
}

func NoContent(w http.ResponseWriter) {
response.NoContent(w)
}

func BadRequest(w http.ResponseWriter, message string) {
response.BadRequest(w, message)
}

func NotFound(w http.ResponseWriter, message string) {
response.NotFound(w, message)
}

func InternalError(w http.ResponseWriter, message string) {
response.InternalError(w, message)
}

func Conflict(w http.ResponseWriter, message string) {
response.Conflict(w, message)
}
