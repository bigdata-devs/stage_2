package main

import (
	"fmt"
	"os"
	"strings"

	"boogle-engine/internal/api"
)

// corsEnvironmentVariable names the browser origins allowed to call the Search API cross-origin.
const corsEnvironmentVariable = "CORS_ALLOWED_ORIGIN"

// corsPolicyFromEnvironment reads CORS_ALLOWED_ORIGIN: "*" for any origin, or a comma-separated
// list of origins such as "https://boogle.example.com,http://localhost:5173". Unset or blank
// falls back to any origin, which is safe for this public, read-only API that uses no
// credentials. A value that names no valid origin is an error, so the service refuses to start
// instead of silently blocking the front-end.
func corsPolicyFromEnvironment() (api.CORSPolicy, error) {
	setting := strings.TrimSpace(os.Getenv(corsEnvironmentVariable))
	if setting == "" {
		setting = api.AnyOrigin
	}
	policy, err := api.NewCORSPolicy(setting)
	if err != nil {
		return api.CORSPolicy{}, fmt.Errorf("%s: %w", corsEnvironmentVariable, err)
	}
	return policy, nil
}
