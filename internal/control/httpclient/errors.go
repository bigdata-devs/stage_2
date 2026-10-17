// Package httpclient calls the internal HTTP APIs of the Boogle Engine services.
// It is the only package of the Control Module that knows their base URLs and routes.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	// ErrTransient marks a failure that may succeed if the same request is repeated later:
	// unreachable service, timeout, throttling (HTTP 408/429), server error or unreadable answer.
	ErrTransient = errors.New("transient service error")
	// ErrPermanent marks a failure that repetition cannot fix, such as any other HTTP 4xx.
	ErrPermanent = errors.New("permanent service error")
)

const maxErrorBody = 512

// doJSON sends one bodyless HTTP request and decodes the JSON answer into out.
// Transport failures and non-2xx answers wrap ErrTransient or ErrPermanent.
func doJSON(ctx context.Context, httpClient *http.Client, method string, url string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: build %s %s: %w", ErrPermanent, method, url, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", ErrTransient, method, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusFailure(response, method, url)
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode answer of %s %s: %w", ErrTransient, method, url, err)
	}
	return nil
}

// statusFailure wraps a non-2xx answer with the sentinel that matches its status code.
func statusFailure(response *http.Response, method string, url string) error {
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	return fmt.Errorf("%w: %s %s answered %s: %s",
		statusCodeError(response.StatusCode), method, url, response.Status, strings.TrimSpace(string(snippet)))
}

// statusCodeError maps an HTTP status to a sentinel: timeouts and throttling stay transient,
// the remaining client errors (4xx) are permanent, and everything else is transient.
func statusCodeError(statusCode int) error {
	isThrottling := statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests
	if statusCode >= 400 && statusCode < 500 && !isThrottling {
		return ErrPermanent
	}
	return ErrTransient
}
