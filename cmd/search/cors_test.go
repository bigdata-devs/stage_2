package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"boogle-engine/internal/api"
	"boogle-engine/internal/models"
	"boogle-engine/internal/search"
)

func TestCORSPolicyFromEnvironment(t *testing.T) {
	cases := []struct {
		name    string
		value   *string
		want    []string
		wantErr bool
	}{
		{name: "unset falls back to any origin", value: nil, want: []string{"*"}},
		{name: "blank falls back to any origin", value: ptr("   "), want: []string{"*"}},
		{name: "explicit wildcard", value: ptr("*"), want: []string{"*"}},
		{
			name:  "comma-separated list",
			value: ptr("https://boogle.example.com, http://localhost:5173"),
			want:  []string{"https://boogle.example.com", "http://localhost:5173"},
		},
		{name: "invalid origin", value: ptr("boogle.example.com"), wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setCORSEnvironment(t, testCase.value)

			policy, err := corsPolicyFromEnvironment()
			if testCase.wantErr {
				if !errors.Is(err, api.ErrInvalidAllowedOrigins) || !strings.Contains(err.Error(), corsEnvironmentVariable) {
					t.Errorf("error = %v, want ErrInvalidAllowedOrigins naming %s", err, corsEnvironmentVariable)
				}
				return
			}
			if err != nil {
				t.Fatalf("corsPolicyFromEnvironment: %v", err)
			}
			if got := policy.AllowedOrigins(); !slices.Equal(got, testCase.want) {
				t.Errorf("AllowedOrigins() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSearchRoutesAnswerCrossOriginBrowsers(t *testing.T) {
	setCORSEnvironment(t, ptr("https://boogle.example.com"))
	cors, err := corsPolicyFromEnvironment()
	if err != nil {
		t.Fatalf("corsPolicyFromEnvironment: %v", err)
	}
	index := &fakeIndex{page: search.Page{Total: 1, Books: []models.BookMetadata{{BookID: 2701, Title: "Moby Dick"}}}}
	router := cors.Wrap(newTestRouter(t, index))
	cases := []struct {
		name       string
		method     string
		target     string
		origin     string
		preflight  bool
		wantStatus int
		wantOrigin string
	}{
		{name: "preflight for search", method: http.MethodOptions, target: "/search?q=whale", origin: "https://boogle.example.com",
			preflight: true, wantStatus: http.StatusNoContent, wantOrigin: "https://boogle.example.com"},
		{name: "search results", method: http.MethodGet, target: "/search?q=whale", origin: "https://boogle.example.com",
			wantStatus: http.StatusOK, wantOrigin: "https://boogle.example.com"},
		{name: "search validation error stays readable", method: http.MethodGet, target: "/search?q=1813", origin: "https://boogle.example.com",
			wantStatus: http.StatusBadRequest, wantOrigin: "https://boogle.example.com"},
		{name: "catalog filters", method: http.MethodGet, target: "/search/filters", origin: "https://boogle.example.com",
			wantStatus: http.StatusOK, wantOrigin: "https://boogle.example.com"},
		{name: "preflight from another origin", method: http.MethodOptions, target: "/search?q=whale", origin: "https://evil.example.net",
			preflight: true, wantStatus: http.StatusForbidden, wantOrigin: ""},
		{name: "same-origin request", method: http.MethodGet, target: "/search?q=whale", origin: "",
			wantStatus: http.StatusOK, wantOrigin: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.target, nil)
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			if testCase.preflight {
				request.Header.Set("Access-Control-Request-Method", http.MethodGet)
				request.Header.Set("Access-Control-Request-Headers", "content-type")
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != testCase.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, testCase.wantOrigin)
			}
		})
	}
}

func setCORSEnvironment(t *testing.T, value *string) {
	t.Helper()
	t.Setenv(corsEnvironmentVariable, "")
	if value == nil {
		if err := os.Unsetenv(corsEnvironmentVariable); err != nil {
			t.Fatalf("unset %s: %v", corsEnvironmentVariable, err)
		}
		return
	}
	t.Setenv(corsEnvironmentVariable, *value)
}

func ptr(value string) *string {
	return &value
}
