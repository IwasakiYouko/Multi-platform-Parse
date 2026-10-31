package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerRejectsUnknownPathsAndMethods(t *testing.T) {
	handler := Handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/assets/", http.StatusNotFound},
		{http.MethodGet, "/index.html", http.StatusNotFound},
		{http.MethodGet, "/api/config", http.StatusNotFound},
		{http.MethodGet, "/../handler.go", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodHead, "/", http.StatusOK},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD returned response body")
			}
		})
	}
}
