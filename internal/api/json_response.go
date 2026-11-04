package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

const internalErrorCode = "internal_error"

type ErrorResponse struct {
	Error   string `json:"error" example:"invalid_parameter"`
	Message string `json:"message" example:"query parameter \"limit\" must be an integer between 1 and 100"`
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		slog.Warn("response not delivered", "status", status, "error", err)
	}
}
