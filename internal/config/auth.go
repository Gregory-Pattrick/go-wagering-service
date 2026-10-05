package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
)

type AuthConfig struct {
	Issuer          string
	Audience        string
	JWKSURL         string
	ProviderClients map[string]string
	InternalClient  string
}

func LoadAuth() (AuthConfig, error) {
	return loadAuth(os.LookupEnv)
}

func loadAuth(lookup func(string) (string, bool)) (AuthConfig, error) {
	cfg := AuthConfig{
		Issuer:         valueOrDefault(lookup, "OIDC_ISSUER", "http://localhost:8081/realms/wagering"),
		Audience:       valueOrDefault(lookup, "OIDC_AUDIENCE", "wagering-api"),
		JWKSURL:        valueOrDefault(lookup, "OIDC_JWKS_URL", "http://localhost:8081/realms/wagering/protocol/openid-connect/certs"),
		InternalClient: valueOrDefault(lookup, "OIDC_INTERNAL_CLIENT", "wallet-service"),
	}
	raw := valueOrDefault(lookup, "OIDC_PROVIDER_CLIENTS", `{"provider-a":"provider-a","provider-b":"provider-b"}`)
	if err := json.Unmarshal([]byte(raw), &cfg.ProviderClients); err != nil {
		return AuthConfig{}, errors.New("OIDC_PROVIDER_CLIENTS must be a JSON object mapping client IDs to provider IDs")
	}
	if err := cfg.Validate(); err != nil {
		return AuthConfig{}, err
	}
	return cfg, nil
}

func (cfg AuthConfig) Validate() error {
	for _, entry := range []struct{ name, value string }{
		{"OIDC_ISSUER", cfg.Issuer}, {"OIDC_JWKS_URL", cfg.JWKSURL},
	} {
		parsed, err := url.Parse(entry.value)
		if err != nil || strings.TrimSpace(entry.value) != entry.value || parsed == nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New(entry.name + " must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
	}
	if strings.TrimSpace(cfg.Audience) == "" || strings.TrimSpace(cfg.Audience) != cfg.Audience {
		return errors.New("OIDC_AUDIENCE must be a nonempty value without surrounding whitespace")
	}
	if strings.TrimSpace(cfg.InternalClient) == "" || strings.TrimSpace(cfg.InternalClient) != cfg.InternalClient {
		return errors.New("OIDC_INTERNAL_CLIENT must be a nonempty value without surrounding whitespace")
	}
	if len(cfg.ProviderClients) == 0 {
		return errors.New("OIDC_PROVIDER_CLIENTS must contain at least one provider client")
	}
	for client, provider := range cfg.ProviderClients {
		if strings.TrimSpace(client) == "" || strings.TrimSpace(provider) == "" ||
			strings.TrimSpace(client) != client || strings.TrimSpace(provider) != provider || client == cfg.InternalClient {
			return errors.New("OIDC_PROVIDER_CLIENTS contains an invalid or conflicting identity")
		}
	}
	return nil
}
