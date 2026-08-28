package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerAuth returns middleware that requires an `Authorization: Bearer <token>`
// header matching one of tokens, compared in constant time. If tokens is empty
// the middleware is a no-op — the service is open, and an upstream gateway or
// mesh is expected to handle authentication (see SECURITY.md).
func bearerAuth(tokens []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t = strings.TrimSpace(t); t != "" {
			allowed[t] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || !constantTimeContains(allowed, token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="ledger"`)
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// constantTimeContains checks membership without leaking which token matched (or
// how far the comparison got) through timing.
func constantTimeContains(set map[string]struct{}, candidate string) bool {
	match := 0
	for known := range set {
		match |= subtle.ConstantTimeCompare([]byte(known), []byte(candidate))
	}
	return match == 1
}
