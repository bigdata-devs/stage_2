package main

import (
	"fmt"
	"os"
	"strings"

	"boogle-engine/internal/api/searchapi"
)

const corsEnvironmentVariable = "CORS_ALLOWED_ORIGIN"

func corsPolicyFromEnvironment() (searchapi.CORSPolicy, error) {
	setting := strings.TrimSpace(os.Getenv(corsEnvironmentVariable))
	if setting == "" {
		setting = searchapi.AnyOrigin
	}
	policy, err := searchapi.NewCORSPolicy(setting)
	if err != nil {
		return searchapi.CORSPolicy{}, fmt.Errorf("%s: %w", corsEnvironmentVariable, err)
	}
	return policy, nil
}
