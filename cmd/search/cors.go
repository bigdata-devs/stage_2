package main

import (
	"fmt"
	"os"
	"strings"

	"boogle-engine/internal/api"
)

const corsEnvironmentVariable = "CORS_ALLOWED_ORIGIN"

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
