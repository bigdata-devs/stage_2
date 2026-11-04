package mongodb

import (
	"os"
)

const (
	DefaultURI      = "mongodb://localhost:27017"
	DefaultDatabase = "boogle"

	uriEnvironmentVariable      = "MONGO_URI"
	databaseEnvironmentVariable = "MONGO_DATABASE"
)

type Config struct {
	URI      string
	Database string
}

func ConfigFromEnvironment() Config {
	return Config{
		URI:      readEnvironmentOr(uriEnvironmentVariable, DefaultURI),
		Database: readEnvironmentOr(databaseEnvironmentVariable, DefaultDatabase),
	}
}

func readEnvironmentOr(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
