package mongodb

import "testing"

// The cases check the documented defaults and the environment overrides of Config, mirroring the
// configuration rules the assignment requires (no hard-coded hosts).

func TestConfigFromEnvironmentUsesDefaultsWhenUnset(t *testing.T) {
	t.Setenv(uriEnvironmentVariable, "")
	t.Setenv(databaseEnvironmentVariable, "")

	config := ConfigFromEnvironment()

	if config.URI != DefaultURI {
		t.Fatalf("expected default URI %q, got %q", DefaultURI, config.URI)
	}
	if config.Database != DefaultDatabase {
		t.Fatalf("expected default database %q, got %q", DefaultDatabase, config.Database)
	}
}

func TestConfigFromEnvironmentHonoursOverrides(t *testing.T) {
	t.Setenv(uriEnvironmentVariable, "mongodb://db.internal:27018")
	t.Setenv(databaseEnvironmentVariable, "boogle_test")

	config := ConfigFromEnvironment()

	if config.URI != "mongodb://db.internal:27018" {
		t.Fatalf("expected overridden URI, got %q", config.URI)
	}
	if config.Database != "boogle_test" {
		t.Fatalf("expected overridden database, got %q", config.Database)
	}
}
