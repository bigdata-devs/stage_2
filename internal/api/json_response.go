package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// internalErrorCode is the ErrorResponse code of every unexpected server-side failure.
const internalErrorCode = "internal_error"

// ErrorResponse is the body of every 4xx and 5xx answer of the Boogle Engine APIs. Error is a
// stable, machine-readable code (each endpoint lists its codes in the specification) and
// Message explains it to a human.
type ErrorResponse struct {
	Error   string `json:"error" example:"invalid_parameter"`
	Message string `json:"message" example:"query parameter \"limit\" must be an integer between 1 and 100"`
}

// writeJSON sends payload as a JSON body with the given status code.
func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		slog.Warn("response not delivered", "status", status, "error", err)
	}
}
