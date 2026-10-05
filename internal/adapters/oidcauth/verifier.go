package oidcauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"
)

type Verifier struct {
	verifier       *oidc.IDTokenVerifier
	providers      map[string]string
	internalClient string
}

func NewVerifier(lifecycle fx.Lifecycle, cfg config.AuthConfig, logger *slog.Logger) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{
		Transport:     transport,
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	keyContext := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(keyContext, cfg.JWKSURL)
	providers := make(map[string]string, len(cfg.ProviderClients))
	for clientID, providerID := range cfg.ProviderClients {
		providers[clientID] = providerID
	}
	result := &Verifier{
		verifier: oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
			ClientID:             cfg.Audience,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
		providers:      providers,
		internalClient: cfg.InternalClient,
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// Probe the trusted endpoint before accepting requests. RemoteKeySet manages
			// its own signature-verification cache and key refreshes at runtime.
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.JWKSURL, nil)
			if err != nil {
				return errors.New("create OIDC key probe")
			}
			response, err := client.Do(request)
			if err != nil {
				return errors.New("OIDC key endpoint is unavailable")
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return errors.New("OIDC key endpoint returned an unsuccessful status")
			}
			var document struct {
				Keys []struct {
					KeyType   string `json:"kty"`
					Use       string `json:"use"`
					Algorithm string `json:"alg"`
					Modulus   string `json:"n"`
					Exponent  string `json:"e"`
				} `json:"keys"`
			}
			if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
				return errors.New("OIDC key endpoint returned invalid JSON")
			}
			usable := false
			for _, key := range document.Keys {
				if key.KeyType == "RSA" && (key.Use == "" || key.Use == "sig") &&
					(key.Algorithm == "" || key.Algorithm == "RS256") && key.Modulus != "" && key.Exponent != "" {
					usable = true
				}
			}
			if !usable {
				return errors.New("OIDC key endpoint has no RS256 signing keys")
			}
			logger.InfoContext(ctx, "OIDC key endpoint available")
			return nil
		},
		OnStop: func(context.Context) error { transport.CloseIdleConnections(); return nil },
	})
	return result, nil
}

func (v *Verifier) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	if v == nil || v.verifier == nil || raw == "" || len(raw) > 16384 {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	token, err := v.verifier.Verify(verifyCtx, raw)
	if err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	var claims struct {
		Type       string `json:"typ"`
		ClientID   string `json:"azp"`
		ActorType  string `json:"actor_type"`
		ProviderID string `json:"provider_id"`
		ExpiresAt  int64  `json:"exp"`
		NotBefore  int64  `json:"nbf"`
		IssuedAt   int64  `json:"iat"`
	}
	if err := token.Claims(&claims); err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	now := time.Now().Unix()
	// Keycloak access tokens use the signed payload claim typ=Bearer.
	// ID tokens and refresh tokens are not API credentials.
	if claims.Type != "Bearer" || claims.ExpiresAt <= now || claims.NotBefore > now ||
		claims.IssuedAt <= 0 || claims.IssuedAt > now+30 || claims.IssuedAt >= claims.ExpiresAt {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	if claims.ActorType == "internal" && claims.ClientID == v.internalClient && claims.ProviderID == "" {
		return auth.NewInternal(claims.ClientID)
	}
	provider, allowed := v.providers[claims.ClientID]
	if claims.ActorType != "provider" || !allowed || claims.ProviderID != provider {
		return auth.Principal{}, auth.ErrForbidden
	}
	return auth.NewProvider(claims.ClientID, provider)
}
