package oidcauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.uber.org/fx"
)

type testLifecycle struct{ hooks []fx.Hook }

func (l *testLifecycle) Append(hook fx.Hook) { l.hooks = append(l.hooks, hook) }

func startTestVerifier(t *testing.T, cfg config.AuthConfig) *Verifier {
	t.Helper()
	lifecycle := &testLifecycle{}
	verifier, err := NewVerifier(lifecycle, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := len(lifecycle.hooks) - 1; i >= 0; i-- {
			if stop := lifecycle.hooks[i].OnStop; stop != nil {
				if err := stop(ctx); err != nil {
					t.Error(err)
				}
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hook := range lifecycle.hooks {
		if hook.OnStart != nil {
			if err := hook.OnStart(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	return verifier
}

func testConfig(url string) config.AuthConfig {
	return config.AuthConfig{
		Issuer: "https://issuer.example/realms/wagering", Audience: "wagering-api", JWKSURL: url,
		ProviderClients: map[string]string{"provider-a": "provider-a", "provider-b": "provider-b"},
		InternalClient:  "wallet-service",
	}
}

func validClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss": "https://issuer.example/realms/wagering", "aud": "wagering-api", "sub": "service-account-a",
		"exp": now + 300, "iat": now - 1, "typ": "Bearer", "azp": "provider-a",
		"actor_type": "provider", "provider_id": "provider-a",
	}
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid, algorithm string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": algorithm, "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func keyDocument(key *rsa.PrivateKey, kid string) map[string]any {
	return map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
	}}}
}

func TestVerifierRejectsInvalidTokensAndIdentities(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keyDocument(key, "key-1"))
	}))
	defer server.Close()
	verifier := startTestVerifier(t, testConfig(server.URL))
	tests := []struct {
		name       string
		change     func(map[string]any)
		signingKey *rsa.PrivateKey
		algorithm  string
		want       error
	}{
		{name: "valid provider"},
		{name: "other provider", change: func(c map[string]any) { c["azp"] = "provider-b"; c["provider_id"] = "provider-b" }},
		{name: "valid internal", change: func(c map[string]any) {
			c["azp"] = "wallet-service"
			c["actor_type"] = "internal"
			delete(c, "provider_id")
		}},
		{name: "wrong issuer", change: func(c map[string]any) { c["iss"] = "https://attacker.example" }, want: auth.ErrUnauthenticated},
		{name: "wrong audience", change: func(c map[string]any) { c["aud"] = "another-api" }, want: auth.ErrUnauthenticated},
		{name: "missing audience", change: func(c map[string]any) { delete(c, "aud") }, want: auth.ErrUnauthenticated},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Unix() - 60 }, want: auth.ErrUnauthenticated},
		{name: "missing expiration", change: func(c map[string]any) { delete(c, "exp") }, want: auth.ErrUnauthenticated},
		{name: "future nbf", change: func(c map[string]any) { c["nbf"] = time.Now().Unix() + 3600 }, want: auth.ErrUnauthenticated},
		{name: "future iat", change: func(c map[string]any) { c["iat"] = time.Now().Unix() + 3600 }, want: auth.ErrUnauthenticated},
		{name: "ID token", change: func(c map[string]any) { c["typ"] = "ID" }, want: auth.ErrUnauthenticated},
		{name: "missing type", change: func(c map[string]any) { delete(c, "typ") }, want: auth.ErrUnauthenticated},
		{name: "invalid signature", signingKey: wrongKey, want: auth.ErrUnauthenticated},
		{name: "disallowed algorithm", algorithm: "HS256", want: auth.ErrUnauthenticated},
		{name: "unsigned algorithm", algorithm: "none", want: auth.ErrUnauthenticated},
		{name: "unknown client", change: func(c map[string]any) { c["azp"] = "unknown" }, want: auth.ErrForbidden},
		{name: "provider spoofing", change: func(c map[string]any) { c["provider_id"] = "provider-b" }, want: auth.ErrForbidden},
		{name: "internal escalation", change: func(c map[string]any) { c["actor_type"] = "internal"; delete(c, "provider_id") }, want: auth.ErrForbidden},
		{name: "missing provider", change: func(c map[string]any) { delete(c, "provider_id") }, want: auth.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			if tc.change != nil {
				tc.change(claims)
			}
			signingKey := tc.signingKey
			if signingKey == nil {
				signingKey = key
			}
			algorithm := tc.algorithm
			if algorithm == "" {
				algorithm = "RS256"
			}
			token := signToken(t, signingKey, "key-1", algorithm, claims)
			principal, err := verifier.Verify(context.Background(), token)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got error %v, want %v", err, tc.want)
			}
			if tc.want == nil && principal.ClientID() != claims["azp"] {
				t.Fatal("incorrect client identity")
			}
		})
	}
	if _, err := verifier.Verify(context.Background(), "malformed"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("malformed token accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifier.Verify(ctx, signToken(t, key, "key-1", "RS256", validClaims())); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("canceled verification accepted")
	}
}

func TestVerifierRefreshesKeysAndUsesCache(t *testing.T) {
	first, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	currentKey, currentID := first, "first"
	available := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if !available {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keyDocument(currentKey, currentID))
	}))
	defer server.Close()
	verifier := startTestVerifier(t, testConfig(server.URL))
	firstToken := signToken(t, first, "first", "RS256", validClaims())
	if _, err := verifier.Verify(context.Background(), firstToken); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	currentKey = second
	currentID = "second"
	mutex.Unlock()
	secondToken := signToken(t, second, "second", "RS256", validClaims())
	if _, err := verifier.Verify(context.Background(), secondToken); err != nil {
		t.Fatalf("key rotation failed: %v", err)
	}
	mutex.Lock()
	available = false
	mutex.Unlock()
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := verifier.Verify(context.Background(), secondToken); err != nil {
				t.Errorf("cached verification failed: %v", err)
			}
		}()
	}
	workers.Wait()
	if _, err := verifier.Verify(context.Background(), firstToken); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("unavailable uncached key did not fail closed")
	}
}

func TestVerifierStartupRejectsUnavailableKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	lifecycle := &testLifecycle{}
	_, err := NewVerifier(lifecycle, testConfig(server.URL), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lifecycle.hooks[0].OnStart(ctx); err == nil {
		t.Fatal("startup succeeded without key endpoint")
	}
	_ = lifecycle.hooks[0].OnStop(ctx)
}
