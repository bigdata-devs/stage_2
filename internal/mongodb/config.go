// Package mongodb implements the datamart storage of Boogle Engine on top of MongoDB. It
// replaces the Stage 1 SQLite metadata datamart and the monolithic JSON inverted index with two
// collections: one document per book for metadata, and one document per term for postings. The
// Indexing Service writes through it and the Search Service reads through it, so every rule the
// package enforces (idempotent updates, atomic publication of a finished book) is what keeps the
// two services consistent without sharing a process.
package mongodb

import (
	"os"
)

// Defaults used when the matching environment variable is missing or empty.
const (
	DefaultURI      = "mongodb://localhost:27017"
	DefaultDatabase = "boogle"

	uriEnvironmentVariable      = "MONGO_URI"
	databaseEnvironmentVariable = "MONGO_DATABASE"
)

// Config carries the connection settings of the datamart. Every field is overridable through an
// environment variable so no host, port or database name is hard-coded, which is what lets the
// same binary move to the Stage 3 cluster unchanged.
type Config struct {
	URI      string
	Database string
}

// ConfigFromEnvironment reads MONGO_URI and MONGO_DATABASE, falling back to the documented
// defaults for anything unset.
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
