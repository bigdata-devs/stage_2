package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"boogle-engine/internal/models"
)

const maxErrorBody = 512

func doJSON(ctx context.Context, httpClient *http.Client, method string, url string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: build %s %s: %w", models.ErrPermanent, method, url, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", models.ErrTransient, method, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusFailure(response, method, url)
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode answer of %s %s: %w", models.ErrTransient, method, url, err)
	}
	return nil
}

func statusFailure(response *http.Response, method string, url string) error {
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	return fmt.Errorf("%w: %s %s answered %s: %s",
		statusCodeError(response.StatusCode), method, url, response.Status, strings.TrimSpace(string(snippet)))
}

func statusCodeError(statusCode int) error {
	isThrottling := statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests
	if statusCode >= 400 && statusCode < 500 && !isThrottling {
		return models.ErrPermanent
	}
	return models.ErrTransient
}
