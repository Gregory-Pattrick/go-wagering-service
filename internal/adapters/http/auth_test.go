package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
)

type stubVerifier struct {
	principal auth.Principal
	err       error
	calls     int
}

func (v *stubVerifier) Verify(context.Context, string) (auth.Principal, error) {
	v.calls++
	return v.principal, v.err
}

func TestAuthenticationHeadersAndPublicHealth(t *testing.T) {
	principal, err := auth.NewProvider("provider-a", "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, method string
		headers            []string
		want               int
		calls              int
	}{
		{"public liveness", "/health/live", "GET", nil, 204, 0},
		{"public liveness HEAD", "/health/live", "HEAD", nil, 204, 0},
		{"public readiness", "/health/ready", "GET", nil, 204, 0},
		{"health write protected", "/health/live", "POST", nil, 401, 0},
		{"health suffix protected", "/health/live/extra", "GET", nil, 401, 0},
		{"missing token", "/private", "GET", nil, 401, 0},
		{"basic authentication", "/private", "GET", []string{"Basic abc"}, 401, 0},
		{"empty bearer", "/private", "GET", []string{"Bearer"}, 401, 0},
		{"duplicate header", "/private", "GET", []string{"Bearer one", "Bearer two"}, 401, 0},
		{"extra token", "/private", "GET", []string{"Bearer one two"}, 401, 0},
		{"valid bearer", "/private", "GET", []string{"Bearer token"}, 204, 1},
		{"case insensitive scheme", "/private", "GET", []string{"bearer token"}, 204, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &stubVerifier{principal: principal}
			middleware := &Authentication{verifier: verifier}
			handler := middleware.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.calls > 0 {
					if got, ok := auth.FromContext(r.Context()); !ok || got.ProviderID() != "provider-a" {
						t.Error("principal missing from context")
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(tc.method, tc.path, nil)
			for _, value := range tc.headers {
				request.Header.Add("Authorization", value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("got %d, want %d", recorder.Code, tc.want)
			}
			if verifier.calls != tc.calls {
				t.Fatalf("verifier called %d times, want %d", verifier.calls, tc.calls)
			}
			if tc.want == 401 && recorder.Header().Get("WWW-Authenticate") == "" {
				t.Error("missing bearer challenge")
			}
		})
	}
}

func TestAuthorizationPolicies(t *testing.T) {
	provider, _ := auth.NewProvider("provider-a", "provider-a")
	internal, _ := auth.NewInternal("wallet-service")
	success := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, tc := range []struct {
		name      string
		principal auth.Principal
		handler   http.Handler
		want      int
	}{
		{"provider denied wallet", provider, RequireInternal(success), 403},
		{"internal allowed wallet", internal, RequireInternal(success), 204},
		{"provider allowed wagering", provider, RequireProvider(success), 204},
		{"internal denied wagering", internal, RequireProvider(success), 403},
		{"missing identity denied", auth.Principal{}, RequireInternal(success), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/resource", nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), tc.principal))
			recorder := httptest.NewRecorder()
			tc.handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("got %d, want %d", recorder.Code, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{auth.ErrUnauthenticated, 401}, {auth.ErrForbidden, 403}} {
		middleware := &Authentication{verifier: &stubVerifier{err: tc.err}}
		request := httptest.NewRequest("GET", "/private", nil)
		request.Header.Set("Authorization", "Bearer token")
		recorder := httptest.NewRecorder()
		middleware.Protect(success).ServeHTTP(recorder, request)
		if recorder.Code != tc.want {
			t.Errorf("got %d, want %d", recorder.Code, tc.want)
		}
	}
}
