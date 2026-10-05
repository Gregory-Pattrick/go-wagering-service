package auth

import (
	"context"
	"errors"
	"testing"
)

func TestPrincipalIsolation(t *testing.T) {
	provider, err := NewProvider("client-a", "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.AuthorizeProvider("provider-a"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(provider.AuthorizeProvider("provider-b"), ErrForbidden) {
		t.Fatal("cross-provider access allowed")
	}
	internal, err := NewInternal("wallet-service")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(internal.AuthorizeProvider("provider-a"), ErrForbidden) {
		t.Fatal("internal identity treated as a provider")
	}
	var zero Principal
	if zero.IsInternal() || zero.IsProvider() {
		t.Fatal("zero principal is authorized")
	}
	if _, ok := FromContext(WithPrincipal(context.Background(), zero)); ok {
		t.Fatal("zero principal accepted from context")
	}
	if _, err := NewProvider("client", ""); err == nil {
		t.Fatal("empty provider accepted")
	}
	if _, err := NewInternal(""); err == nil {
		t.Fatal("empty internal client accepted")
	}
}
