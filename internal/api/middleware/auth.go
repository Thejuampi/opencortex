package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"opencortex/internal/service"
)

func RequireAuth(app *service.App) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r.Header.Get("Authorization"))
			if token == "" {
				token = strings.TrimSpace(r.URL.Query().Get("api_key"))
			}
			authCtx, err := app.Authenticate(r.Context(), token)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid API key")
				return
			}
			next.ServeHTTP(w, r.WithContext(service.WithAuthContext(r.Context(), authCtx)))
		})
	}
}

func RequirePermission(app *service.App, resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCtx, ok := service.AuthFromContext(r.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
				return
			}
			if err := app.Authorize(authCtx, resource, action); err != nil {
				writeErr(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearer(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if strings.HasPrefix(strings.ToLower(h), strings.ToLower(prefix)) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return h
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         false,
		"data":       nil,
		"error":      map[string]any{"code": code, "message": message},
		"pagination": nil,
	})
}
