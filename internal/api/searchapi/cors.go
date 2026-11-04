package searchapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"boogle-engine/internal/api"
)

const AnyOrigin = "*"

const (
	corsAllowedMethods   = "GET, HEAD, OPTIONS"
	corsAllowedHeaders   = "Content-Type"
	corsMaxAgeSeconds    = "600"
	originNotAllowedCode = "origin_not_allowed"
)

var ErrInvalidAllowedOrigins = errors.New("invalid allowed origins")

type CORSPolicy struct {
	anyOrigin bool
	origins   []string
}

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

func (policy CORSPolicy) AllowedOrigins() []string {
	if policy.anyOrigin {
		return []string{AnyOrigin}
	}
	return slices.Clone(policy.origins)
}

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
			api.WriteJSON(writer, http.StatusForbidden, api.ErrorResponse{
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
