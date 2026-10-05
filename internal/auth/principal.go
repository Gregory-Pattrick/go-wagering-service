package auth

import (
	"context"
	"errors"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("access forbidden")
)

// Principal contains identity derived exclusively from a verified access token.
type Principal struct {
	clientID   string
	providerID string
	internal   bool
}

func NewProvider(clientID, providerID string) (Principal, error) {
	if clientID == "" || providerID == "" {
		return Principal{}, ErrForbidden
	}
	return Principal{clientID: clientID, providerID: providerID}, nil
}

func NewInternal(clientID string) (Principal, error) {
	if clientID == "" {
		return Principal{}, ErrForbidden
	}
	return Principal{clientID: clientID, internal: true}, nil
}

func (p Principal) ClientID() string   { return p.clientID }
func (p Principal) ProviderID() string { return p.providerID }
func (p Principal) IsInternal() bool   { return p.clientID != "" && p.internal }
func (p Principal) IsProvider() bool   { return p.clientID != "" && p.providerID != "" && !p.internal }

func (p Principal) AuthorizeProvider(providerID string) error {
	if !p.IsProvider() || providerID == "" || p.providerID != providerID {
		return ErrForbidden
	}
	return nil
}

type Verifier interface {
	Verify(context.Context, string) (Principal, error)
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok && (principal.IsProvider() || principal.IsInternal())
}
