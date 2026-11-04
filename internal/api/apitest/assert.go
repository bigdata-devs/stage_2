package apitest

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"boogle-engine/internal/api"
)

func DecodeBody[Body any](t *testing.T, recorder *httptest.ResponseRecorder) Body {
	t.Helper()
	var body Body
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, recorder.Body.String())
	}
	return body
}

func RawField(t *testing.T, recorder *httptest.ResponseRecorder, name string) string {
	t.Helper()
	fields := DecodeBody[map[string]json.RawMessage](t, recorder)
	field, found := fields[name]
	if !found {
		t.Fatalf("response has no %q field: %s", name, recorder.Body.String())
	}
	return string(field)
}

func AssertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, want, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
}

func AssertBody(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	if body := strings.TrimSpace(recorder.Body.String()); body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func AssertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	AssertStatus(t, recorder, wantStatus)
	if response := DecodeBody[api.ErrorResponse](t, recorder); response.Error != wantCode {
		t.Errorf("error = %+v, want code %s", response, wantCode)
	}
}
