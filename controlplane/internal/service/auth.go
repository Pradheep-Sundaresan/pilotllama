package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// RFC 6750 bearer-token alphabet, with any '=' padding only at the end.
// Length is a configuration bound, not an entropy guarantee.
func validAuthToken(token string) bool {
	if len(token) < 32 || len(token) > 256 {
		return false
	}
	base := strings.TrimRight(token, "=")
	if len(base) == 0 {
		return false
	}
	for _, c := range base {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/", c)) {
			return false
		}
	}
	return true
}

func tokenDigest(token string) *[32]byte {
	digest := sha256.Sum256([]byte(token))
	return &digest
}

func (s *Service) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authDigest == nil {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) == 1 {
			scheme, token, found := strings.Cut(values[0], " ")
			if found && strings.EqualFold(scheme, "Bearer") && validAuthToken(token) {
				digest := tokenDigest(token)
				if subtle.ConstantTimeCompare(digest[:], s.authDigest[:]) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="inferno"`)
		writeJSON(w, http.StatusUnauthorized, fail("Missing or invalid bearer token", "authentication_error", "invalid_api_key"))
	})
}
