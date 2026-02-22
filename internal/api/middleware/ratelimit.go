package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"opencortex/internal/service"
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*limiterEntry
	rps     rate.Limit
	burst   int
}

func NewRateLimiter(perMinute int, burst int) *RateLimiter {
	if perMinute <= 0 {
		perMinute = 1000
	}
	if burst <= 0 {
		burst = 200
	}
	return &RateLimiter{
		clients: map[string]*limiterEntry{},
		rps:     rate.Limit(float64(perMinute) / 60.0),
		burst:   burst,
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := rl.clientKey(r)
		limiter := rl.getLimiter(key)
		if !limiter.Allow() {
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) clientKey(r *http.Request) string {
	if authCtx, ok := service.AuthFromContext(r.Context()); ok {
		return authCtx.Agent.ID
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (rl *RateLimiter) getLimiter(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if l, ok := rl.clients[key]; ok {
		l.lastSeen = time.Now()
		return l.limiter
	}
	lim := rate.NewLimiter(rl.rps, rl.burst)
	rl.clients[key] = &limiterEntry{limiter: lim, lastSeen: time.Now()}
	return lim
}
