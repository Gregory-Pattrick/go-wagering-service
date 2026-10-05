package config

import "testing"

func TestAuthConfiguration(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	cfg, err := loadAuth(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audience != "wagering-api" || cfg.ProviderClients["provider-a"] != "provider-a" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	for _, tc := range []struct{ name, key, value string }{
		{"empty issuer", "OIDC_ISSUER", ""},
		{"invalid scheme", "OIDC_JWKS_URL", "file:///etc/passwd"},
		{"credentials in URL", "OIDC_JWKS_URL", "https://user:secret@example.com/keys"},
		{"query in URL", "OIDC_JWKS_URL", "https://example.com/keys?token=secret"},
		{"missing host", "OIDC_ISSUER", "https:///realm"},
		{"empty audience", "OIDC_AUDIENCE", ""},
		{"blank internal client", "OIDC_INTERNAL_CLIENT", " "},
		{"invalid JSON", "OIDC_PROVIDER_CLIENTS", "{"},
		{"empty provider map", "OIDC_PROVIDER_CLIENTS", "{}"},
		{"null provider map", "OIDC_PROVIDER_CLIENTS", "null"},
		{"missing provider", "OIDC_PROVIDER_CLIENTS", `{"provider-a":""}`},
		{"identity collision", "OIDC_PROVIDER_CLIENTS", `{"wallet-service":"provider-a"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadAuth(func(key string) (string, bool) {
				if key == tc.key {
					return tc.value, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}
}
