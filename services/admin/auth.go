package admin

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const userContextKey contextKey = "admin-user"

// UserFromContext returns the authenticated admin user name.
func UserFromContext(ctx context.Context) string {
	user, _ := ctx.Value(userContextKey).(string)
	return user
}

// csrfHeader must accompany every state-changing request. Browsers cannot send custom headers
// cross-origin without a CORS preflight, which the admin interface never allows.
const csrfHeader = "X-Podsync-Admin"

// authenticator enforces the configured authentication mode.
type authenticator struct {
	mode         string
	trusted      []*net.IPNet
	userHeader   string
	username     string
	passwordHash []byte
	limiter      *failureLimiter
}

func newAuthenticator(cfg Config) (*authenticator, error) {
	auth := &authenticator{
		mode:         cfg.Auth,
		userHeader:   cfg.UserHeader,
		username:     cfg.Username,
		passwordHash: []byte(cfg.PasswordHash),
		limiter:      newFailureLimiter(10, 5*time.Minute),
	}
	if cfg.Auth == AuthProxy {
		trusted, err := parseTrustedProxies(cfg.TrustedProxies)
		if err != nil {
			return nil, err
		}
		auth.trusted = trusted
	}
	return auth, nil
}

func (a *authenticator) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			user string
			ok   bool
		)
		switch a.mode {
		case AuthProxy:
			user, ok = a.proxyUser(w, r)
		case AuthPassword:
			user, ok = a.passwordUser(w, r)
		default:
			http.Error(w, "admin authentication is not configured", http.StatusInternalServerError)
			return
		}
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
	})
}

// proxyUser accepts the user header only from a trusted proxy. The direct peer address is used,
// never X-Forwarded-For, which clients can set themselves.
func (a *authenticator) proxyUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	ip := peerIP(r)
	if ip == nil || !containsIP(a.trusted, ip) {
		log.WithFields(log.Fields{"remote_addr": r.RemoteAddr, "path": r.URL.Path}).Warn("admin request rejected: not from a trusted proxy")
		http.Error(w, "forbidden: admin requests must come through the configured reverse proxy", http.StatusForbidden)
		return "", false
	}
	user := strings.TrimSpace(r.Header.Get(a.userHeader))
	if user == "" {
		log.WithFields(log.Fields{"remote_addr": r.RemoteAddr, "header": a.userHeader}).Warn("admin request rejected: no authenticated user header")
		http.Error(w, "unauthorized: the reverse proxy did not provide an authenticated user in "+a.userHeader, http.StatusUnauthorized)
		return "", false
	}
	return user, true
}

func (a *authenticator) passwordUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	client := r.RemoteAddr
	if ip := peerIP(r); ip != nil {
		client = ip.String()
	}
	if a.limiter.blocked(client) {
		http.Error(w, "too many failed login attempts; try again later", http.StatusTooManyRequests)
		return "", false
	}
	user, password, ok := r.BasicAuth()
	if ok {
		userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(a.username)) == 1
		// Always run bcrypt so timing does not reveal whether the user name was right.
		passwordMatch := bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)) == nil
		if userMatch && passwordMatch {
			a.limiter.reset(client)
			return user, true
		}
		a.limiter.fail(client)
		log.WithField("remote_addr", r.RemoteAddr).Warn("admin login failed")
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="Podsync admin", charset="UTF-8"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", false
}

// requireSameOrigin rejects state-changing requests that could come from another site.
func requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(csrfHeader) == "" {
			http.Error(w, "forbidden: missing "+csrfHeader+" header", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets restrictive browser policies for the admin interface.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func peerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// failureLimiter blocks a client after too many failed logins within a window.
type failureLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	failures map[string][]time.Time
	now      func() time.Time
}

func newFailureLimiter(limit int, window time.Duration) *failureLimiter {
	return &failureLimiter{limit: limit, window: window, failures: map[string][]time.Time{}, now: time.Now}
}

func (l *failureLimiter) recent(client string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.failures[client][:0]
	for _, at := range l.failures[client] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, client)
		return nil
	}
	l.failures[client] = kept
	return kept
}

func (l *failureLimiter) blocked(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(client)) >= l.limit
}

func (l *failureLimiter) fail(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[client] = append(l.recent(client), l.now())
}

func (l *failureLimiter) reset(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, client)
}
