package httpclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// serveHandler runs a throwaway HTTP server for one test and returns its base URL.
func serveHandler(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

// serveStatus answers every request with a fixed status code and body.
func serveStatus(t *testing.T, statusCode int, body string) string {
	return serveHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(statusCode)
		writer.Write([]byte(body))
	})
}
