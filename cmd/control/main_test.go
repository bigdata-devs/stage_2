package main

import "testing"

func TestParseConfigAcceptsVerifyWithoutBookIDs(t *testing.T) {
	config, err := parseConfig([]string{"-verify", "-search-url", "http://localhost:7003"})

	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !config.verify || config.searchURL != "http://localhost:7003" || len(config.bookIDs) != 0 {
		t.Fatalf("config = %+v, want verify mode against http://localhost:7003 without candidates", config)
	}
}

func TestParseConfigRejectsVerifyWithoutSearchURL(t *testing.T) {
	if _, err := parseConfig([]string{"-verify", "-search-url", ""}); err == nil {
		t.Fatal("parseConfig accepted -verify without a search URL")
	}
}

func TestParseConfigRequiresBookIDsToRunThePipeline(t *testing.T) {
	if _, err := parseConfig([]string{}); err == nil {
		t.Fatal("parseConfig accepted a pipeline run without book IDs")
	}
	config, err := parseConfig([]string{"-ids", "1342,84", "-retry-failed"})
	if err != nil || !config.retryFailed || len(config.bookIDs) != 2 {
		t.Fatalf("parseConfig = %+v, %v; want two candidates with retry-failed", config, err)
	}
}
