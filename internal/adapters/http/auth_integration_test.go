//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.uber.org/fx"
)

func TestKeycloakAuthenticationIntegration(t *testing.T) {
	endpoint := os.Getenv("KEYCLOAK_TOKEN_URL")
	if endpoint == "" {
		t.Fatal("KEYCLOAK_TOKEN_URL is required for integration tests")
	}
	cfg, err := config.LoadAuth()
	if err != nil {
		t.Fatal(err)
	}
	var authentication *Authentication
	app := fx.New(
		fx.Supply(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))),
		fx.Provide(oidcauth.NewVerifier, NewAuthentication),
		fx.Populate(&authentication), fx.NopLogger,
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// These are test-only handlers. No business endpoints are added by this test.
	mux := http.NewServeMux()
	success := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.Handle("GET /health/live", success)
	mux.Handle("POST /wallets", RequireInternal(success))
	mux.Handle("GET /providers/{providerID}/transactions/{transactionID}", RequireProvider(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := auth.FromContext(r.Context())
		if err := principal.AuthorizeProvider(r.PathValue("providerID")); err != nil {
			writeAuthError(w, 403)
			return
		}
		w.WriteHeader(204)
	})))
	server := httptest.NewServer(authentication.Protect(mux))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	tokens := map[string]string{}
	for _, entry := range []struct{ id, secret string }{
		{"provider-a", "provider-a-local-secret"},
		{"provider-b", "provider-b-local-secret"},
		{"wallet-service", "wallet-service-local-secret"},
	} {
		body := url.Values{"grant_type": {"client_credentials"}, "client_id": {entry.id}, "client_secret": {entry.secret}}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(body.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var token struct {
			AccessToken string `json:"access_token"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token)
		response.Body.Close()
		if response.StatusCode != 200 || decodeErr != nil || token.AccessToken == "" {
			t.Fatalf("obtain token for %s: HTTP %d, decode error %v", entry.id, response.StatusCode, decodeErr)
		}
		tokens[entry.id] = token.AccessToken
	}
	for _, tc := range []struct {
		name, method, path, identity string
		want                         int
	}{
		{"public health", "GET", "/health/live", "", 204},
		{"missing token", "POST", "/wallets", "", 401},
		{"malformed token", "POST", "/wallets", "malformed", 401},
		{"provider denied wallet", "POST", "/wallets", "provider-a", 403},
		{"internal allowed wallet", "POST", "/wallets", "wallet-service", 204},
		{"provider A own transaction", "GET", "/providers/provider-a/transactions/example", "provider-a", 204},
		{"provider B own transaction", "GET", "/providers/provider-b/transactions/example", "provider-b", 204},
		{"provider A cross-provider denied", "GET", "/providers/provider-b/transactions/example", "provider-a", 403},
		{"provider B cross-provider denied", "GET", "/providers/provider-a/transactions/example", "provider-b", 403},
		{"internal denied provider route", "GET", "/providers/provider-a/transactions/example", "wallet-service", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			token := tokens[tc.identity]
			if tc.identity == "malformed" {
				token = "not-a-token"
			}
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("got HTTP %d, want %d", response.StatusCode, tc.want)
			}
		})
	}
}
