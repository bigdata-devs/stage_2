package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestNewCORSPolicyNormalizesAllowedOrigins(t *testing.T) {
	cases := []struct {
		setting string
		want    []string
	}{
		{setting: "*", want: []string{"*"}},
		{setting: "  *  ", want: []string{"*"}},
		{setting: "https://boogle.example.com", want: []string{"https://boogle.example.com"}},
		{setting: "HTTPS://Boogle.Example.COM/", want: []string{"https://boogle.example.com"}},
		{setting: "https://boogle.example.com:443,http://localhost:80", want: []string{"https://boogle.example.com", "http://localhost"}},
		{setting: "https://boogle.example.com:8443", want: []string{"https://boogle.example.com:8443"}},
		{
			setting: " http://localhost:5173 , https://boogle.example.com,http://localhost:5173,",
			want:    []string{"http://localhost:5173", "https://boogle.example.com"},
		},
		{setting: "http://[::1]:5173", want: []string{"http://[::1]:5173"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.setting, func(t *testing.T) {
			policy, err := NewCORSPolicy(testCase.setting)
			if err != nil {
				t.Fatalf("NewCORSPolicy: %v", err)
			}
			if got := policy.AllowedOrigins(); !slices.Equal(got, testCase.want) {
				t.Errorf("AllowedOrigins() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNewCORSPolicyRejectsSettingsItCannotEnforce(t *testing.T) {
	for _, setting := range []string{
		"",
		" , ",
		"*,https://boogle.example.com",
		"boogle.example.com",
		"localhost:5173",
		"https://boogle.example.com/app",
		"https://boogle.example.com?env=prod",
		"https://boogle.example.com#top",
		"https://user:secret@boogle.example.com",
		"ftp://boogle.example.com",
		"null",
		"https://",
		"https://:8443",
	} {
		t.Run(setting, func(t *testing.T) {
			if _, err := NewCORSPolicy(setting); !errors.Is(err, ErrInvalidAllowedOrigins) {
				t.Errorf("NewCORSPolicy(%q) error = %v, want ErrInvalidAllowedOrigins", setting, err)
			}
		})
	}
}

// countingHandler stands in for the router: it counts calls and answers with a fixed status.
type countingHandler struct {
	calls  int
	status int
}

func (handler *countingHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	handler.calls++
	writeJSON(writer, handler.status, map[string]string{"served": "by the router"})
}

func serveThroughCORS(t *testing.T, setting string, request *http.Request) (*httptest.ResponseRecorder, *countingHandler) {
	t.Helper()
	policy, err := NewCORSPolicy(setting)
	if err != nil {
		t.Fatalf("NewCORSPolicy: %v", err)
	}
	next := &countingHandler{status: http.StatusOK}
	if strings.Contains(request.URL.RawQuery, "fail") {
		next.status = http.StatusBadRequest
	}
	recorder := httptest.NewRecorder()
	policy.Wrap(next).ServeHTTP(recorder, request)
	return recorder, next
}

func newCORSRequest(method, target, origin string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	return request
}

func newPreflight(origin string) *http.Request {
	request := newCORSRequest(http.MethodOptions, "/search?q=whale", origin)
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	return request
}

func assertHeader(t *testing.T, recorder *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := recorder.Header().Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func assertVaryOrigin(t *testing.T, recorder *httptest.ResponseRecorder, want bool) {
	t.Helper()
	if got := slices.Contains(recorder.Header().Values("Vary"), "Origin"); got != want {
		t.Errorf("Vary: Origin present = %v, want %v (Vary = %q)", got, want, recorder.Header().Values("Vary"))
	}
}

func TestCORSAnyOriginLetsEveryPageReadResponses(t *testing.T) {
	recorder, next := serveThroughCORS(t, AnyOrigin, newCORSRequest(http.MethodGet, "/search?q=whale", "https://any.example.org"))

	if recorder.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("status = %d with %d router calls, want 200 from the router", recorder.Code, next.calls)
	}
	assertHeader(t, recorder, "Access-Control-Allow-Origin", "*")
	assertHeader(t, recorder, "Access-Control-Allow-Credentials", "")
	assertVaryOrigin(t, recorder, false)
}

func TestCORSAllowedOriginCanReadErrorResponses(t *testing.T) {
	recorder, next := serveThroughCORS(t, "https://boogle.example.com", newCORSRequest(http.MethodGet, "/search?fail", "https://boogle.example.com"))

	if recorder.Code != http.StatusBadRequest || next.calls != 1 {
		t.Fatalf("status = %d with %d router calls, want the router's 400", recorder.Code, next.calls)
	}
	assertHeader(t, recorder, "Access-Control-Allow-Origin", "https://boogle.example.com")
}

func TestCORSAnswersPreflightWithoutReachingTheRouter(t *testing.T) {
	for _, setting := range []string{AnyOrigin, "https://boogle.example.com"} {
		t.Run(setting, func(t *testing.T) {
			recorder, next := serveThroughCORS(t, setting, newPreflight("https://boogle.example.com"))

			if recorder.Code != http.StatusNoContent || next.calls != 0 {
				t.Fatalf("status = %d with %d router calls, want 204 answered by the policy", recorder.Code, next.calls)
			}
			wantOrigin := map[string]string{AnyOrigin: "*"}[setting]
			if wantOrigin == "" {
				wantOrigin = "https://boogle.example.com"
			}
			assertHeader(t, recorder, "Access-Control-Allow-Origin", wantOrigin)
			assertHeader(t, recorder, "Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			assertHeader(t, recorder, "Access-Control-Allow-Headers", "Content-Type")
			assertHeader(t, recorder, "Access-Control-Max-Age", "600")
			assertHeader(t, recorder, "Access-Control-Allow-Credentials", "")
			if recorder.Body.Len() != 0 {
				t.Errorf("preflight body = %q, want empty", recorder.Body.String())
			}
		})
	}
}

func TestCORSOriginListEchoesTheMatchingOrigin(t *testing.T) {
	setting := "https://boogle.example.com,http://localhost:5173"
	for _, origin := range []string{"http://localhost:5173", "https://boogle.example.com", "HTTPS://BOOGLE.example.com"} {
		t.Run(origin, func(t *testing.T) {
			recorder, _ := serveThroughCORS(t, setting, newCORSRequest(http.MethodGet, "/search?q=whale", origin))

			assertHeader(t, recorder, "Access-Control-Allow-Origin", origin)
			assertVaryOrigin(t, recorder, true)
		})
	}
}

func TestCORSOriginOutsideTheListIsServedWithoutCORSHeaders(t *testing.T) {
	recorder, next := serveThroughCORS(t, "https://boogle.example.com", newCORSRequest(http.MethodGet, "/search?q=whale", "https://evil.example.net"))

	if recorder.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("status = %d with %d router calls, want the router's 200", recorder.Code, next.calls)
	}
	assertHeader(t, recorder, "Access-Control-Allow-Origin", "")
	assertVaryOrigin(t, recorder, true)
}

func TestCORSRejectsPreflightFromOriginOutsideTheList(t *testing.T) {
	for _, origin := range []string{"https://evil.example.net", "null", "https://boogle.example.com.evil.net"} {
		t.Run(origin, func(t *testing.T) {
			recorder, next := serveThroughCORS(t, "https://boogle.example.com", newPreflight(origin))

			if recorder.Code != http.StatusForbidden || next.calls != 0 {
				t.Fatalf("status = %d with %d router calls, want 403 answered by the policy", recorder.Code, next.calls)
			}
			assertHeader(t, recorder, "Access-Control-Allow-Origin", "")
			assertHeader(t, recorder, "Access-Control-Allow-Methods", "")
			if response := decodeBody[ErrorResponse](t, recorder); response.Error != originNotAllowedCode {
				t.Errorf("error = %+v, want %s", response, originNotAllowedCode)
			}
		})
	}
}

func TestCORSLeavesRequestsWithoutOriginAlone(t *testing.T) {
	for _, setting := range []string{AnyOrigin, "https://boogle.example.com"} {
		t.Run(setting, func(t *testing.T) {
			recorder, next := serveThroughCORS(t, setting, newCORSRequest(http.MethodGet, "/search?q=whale", ""))

			if recorder.Code != http.StatusOK || next.calls != 1 {
				t.Fatalf("status = %d with %d router calls, want the router's 200", recorder.Code, next.calls)
			}
			assertHeader(t, recorder, "Access-Control-Allow-Origin", "")
		})
	}
}

func TestCORSPassesPlainOptionsRequestsToTheRouter(t *testing.T) {
	recorder, next := serveThroughCORS(t, AnyOrigin, newCORSRequest(http.MethodOptions, "/search", "https://boogle.example.com"))

	if next.calls != 1 {
		t.Errorf("router calls = %d, want 1: OPTIONS without Access-Control-Request-Method is not a preflight", next.calls)
	}
	assertHeader(t, recorder, "Access-Control-Allow-Methods", "")
}
