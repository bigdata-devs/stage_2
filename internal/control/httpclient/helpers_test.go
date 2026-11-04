package httpclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func serveHandler(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func serveStatus(t *testing.T, statusCode int, body string) string {
	return serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(statusCode)
		writer.Write([]byte(body))
	})
}
