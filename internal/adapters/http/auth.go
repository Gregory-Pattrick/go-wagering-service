package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
)

type Authentication struct{ verifier auth.Verifier }

func NewAuthentication(verifier *oidcauth.Verifier) *Authentication {
	return &Authentication{verifier: verifier}
}

func (a *Authentication) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			(r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 || len(values[0]) > 16400 {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		principal, err := a.verifier.Verify(r.Context(), parts[1])
		if err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				writeAuthError(w, http.StatusForbidden)
			} else {
				writeAuthError(w, http.StatusUnauthorized)
			}
			return
		}
		if !principal.IsInternal() && !principal.IsProvider() {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

// RequireInternal must wrap every wallet route when business handlers are added.
func RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.FromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		if !principal.IsInternal() {
			writeAuthError(w, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireProvider checks actor type. Resource ownership must also be checked
// in the use case, before returning any transaction or idempotent replay.
func RequireProvider(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.FromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		if !principal.IsProvider() {
			writeAuthError(w, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeAuthError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="wagering-api"`)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{\"error\":{\"code\":\"UNAUTHENTICATED\",\"message\":\"A valid access token is required\"}}\n"))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte("{\"error\":{\"code\":\"FORBIDDEN\",\"message\":\"Access is not permitted\"}}\n"))
}
