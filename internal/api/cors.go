package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// AnyOrigin, as the whole allowed-origins setting, lets a page on any origin call the API from a
// browser. That is safe for the Search API because it is public, read-only and never relies on
// cookies or other credentials: the policy never sends Access-Control-Allow-Credentials.
const AnyOrigin = "*"

const (
	// corsAllowedMethods lists every method the Search API answers; Go's "GET /path" patterns
	// also serve HEAD.
	corsAllowedMethods = "GET, HEAD, OPTIONS"
	// corsAllowedHeaders lists the request headers a cross-origin page may send beyond the
	// CORS-safelisted ones. Front-end HTTP clients often set Content-Type even on a GET.
	corsAllowedHeaders = "Content-Type"
	// corsMaxAgeSeconds lets a browser reuse a preflight answer for ten minutes, short enough for
	// a configuration change to reach every browser quickly.
	corsMaxAgeSeconds = "600"
	// originNotAllowedCode is the ErrorResponse code of a preflight from an origin outside the list.
	originNotAllowedCode = "origin_not_allowed"
)

// ErrInvalidAllowedOrigins reports an allowed-origins setting the policy cannot enforce.
var ErrInvalidAllowedOrigins = errors.New("invalid allowed origins")

// CORSPolicy lets web pages served from other origins call the API from a browser (Cross-Origin
// Resource Sharing). It allows either every origin (AnyOrigin) or an explicit list of origins,
// each written scheme://host[:port] as browsers send it in the Origin header. A policy never
// changes after construction, so one value can wrap a handler that serves every request goroutine.
type CORSPolicy struct {
	anyOrigin bool
	origins   []string
}

// NewCORSPolicy parses an allowed-origins setting: AnyOrigin ("*"), or a comma-separated list
// such as "https://boogle.example.com,http://localhost:5173". Origins compare case-insensitively,
// and a trailing slash or a default port (:80 for http, :443 for https) is dropped, because
// browsers never send them. Anything else that is not a bare http or https origin (a path, a
// query, credentials, "null", or "*" mixed with origins) is rejected, so a typo fails at start-up
// instead of silently blocking the front-end. Empty entries, such as a trailing comma, are skipped.
func NewCORSPolicy(allowedOrigins string) (CORSPolicy, error) {
	policy := CORSPolicy{origins: []string{}}
	for _, entry := range strings.Split(allowedOrigins, ",") {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case entry == AnyOrigin:
			policy.anyOrigin = true
		default:
			origin, err := normalizeOrigin(entry)
			if err != nil {
				return CORSPolicy{}, err
			}
			if !slices.Contains(policy.origins, origin) {
				policy.origins = append(policy.origins, origin)
			}
		}
	}
	if policy.anyOrigin && len(policy.origins) > 0 {
		return CORSPolicy{}, fmt.Errorf("%w: %q cannot be combined with explicit origins", ErrInvalidAllowedOrigins, AnyOrigin)
	}
	if !policy.anyOrigin && len(policy.origins) == 0 {
		return CORSPolicy{}, fmt.Errorf("%w: no origin given; use %q or a comma-separated list of origins", ErrInvalidAllowedOrigins, AnyOrigin)
	}
	return policy, nil
}

// AllowedOrigins returns the setting in normalized form, [AnyOrigin] or the explicit origins in
// the order given, for start-up logs.
func (policy CORSPolicy) AllowedOrigins() []string {
	if policy.anyOrigin {
		return []string{AnyOrigin}
	}
	return slices.Clone(policy.origins)
}

// Wrap returns next guarded by the policy:
//
//   - Every response to an allowed origin, errors included, carries Access-Control-Allow-Origin,
//     so the page can read error bodies as well as results.
//   - A preflight (OPTIONS with Origin and Access-Control-Request-Method) from an allowed origin
//     is answered here with 204 and the allowed methods and headers; it never reaches next.
//   - A preflight from any other origin gets 403 origin_not_allowed.
//   - Any other request from an origin outside the list is served without CORS headers, so the
//     browser withholds the response from that page. Same-origin pages and non-browser clients
//     send no Origin header and are unaffected.
//
// With an explicit list the answer depends on Origin, so every response carries Vary: Origin and
// shared caches keep one copy per origin.
func (policy CORSPolicy) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers := writer.Header()
		if !policy.anyOrigin {
			headers.Add("Vary", "Origin")
		}
		origin := request.Header.Get("Origin")
		allowed := origin != "" && policy.allows(origin)
		if allowed {
			headers.Set("Access-Control-Allow-Origin", policy.allowOriginValue(origin))
		}
		if !isPreflight(request) {
			next.ServeHTTP(writer, request)
			return
		}
		if !allowed {
			writeJSON(writer, http.StatusForbidden, ErrorResponse{
				Error:   originNotAllowedCode,
				Message: fmt.Sprintf("origin %q may not call this API from a browser", origin),
			})
			return
		}
		headers.Set("Access-Control-Allow-Methods", corsAllowedMethods)
		headers.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
		headers.Set("Access-Control-Max-Age", corsMaxAgeSeconds)
		writer.WriteHeader(http.StatusNoContent)
	})
}

func (policy CORSPolicy) allows(origin string) bool {
	return policy.anyOrigin || slices.Contains(policy.origins, strings.ToLower(origin))
}

// allowOriginValue echoes the request's own Origin for an explicit list, because the header names
// exactly one origin and browsers compare it with the one they sent.
func (policy CORSPolicy) allowOriginValue(origin string) string {
	if policy.anyOrigin {
		return AnyOrigin
	}
	return origin
}

func isPreflight(request *http.Request) bool {
	return request.Method == http.MethodOptions &&
		request.Header.Get("Origin") != "" &&
		request.Header.Get("Access-Control-Request-Method") != ""
}

// normalizeOrigin turns one configured entry into the serialization browsers send: lower-case
// scheme and host, no trailing slash and no default port.
func normalizeOrigin(entry string) (string, error) {
	invalid := fmt.Errorf("%w: %q is not an origin; write scheme://host[:port] with http or https, without a path",
		ErrInvalidAllowedOrigins, entry)
	parsed, err := url.Parse(entry)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", invalid
	}
	if parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", invalid
	}
	host := strings.ToLower(parsed.Host)
	if defaultPort := map[string]string{"http": "80", "https": "443"}[parsed.Scheme]; parsed.Port() == defaultPort {
		host = strings.TrimSuffix(host, ":"+defaultPort)
	}
	return parsed.Scheme + "://" + host, nil
}
