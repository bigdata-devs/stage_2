package httpclient

import (
	"context"
	"fmt"
	"net/http"
)

const healthPath = "/health"

// pingService sends GET /health and only inspects the status code: health bodies
// are free-form, so nothing is decoded. Unreachable services wrap ErrTransient
// and unhealthy answers wrap the sentinel matching their status code.
func pingService(ctx context.Context, httpClient *http.Client, baseURL string) error {
	url := baseURL + healthPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: build GET %s: %w", ErrPermanent, url, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: GET %s: %w", ErrTransient, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusFailure(response, http.MethodGet, url)
	}
	return nil
}
