package admin

import (
	"net"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"

	"github.com/mxpv/podsync/pkg/configschema"
)

// Authentication modes for the admin interface.
const (
	// AuthProxy trusts a user header set by an authenticating reverse proxy (e.g. SWAG with
	// Authelia, Authentik or oauth2-proxy for Keycloak), accepted only from trusted proxy addresses.
	AuthProxy = "proxy"
	// AuthPassword uses HTTP Basic authentication against a bcrypt password hash.
	AuthPassword = "password"
)

// Defaults for the admin interface.
const (
	DefaultPort       = 8081
	DefaultUserHeader = "Remote-User"
	DefaultUsername   = "admin"
)

// Config configures the admin interface, a separate listener from the podcast server so that it
// can be protected without affecting public feed and episode URLs.
//
//	[admin]
//	enabled = true
//	port = 8081
//	auth = "proxy"
//	trusted_proxies = ["172.18.0.0/16"]
type Config struct {
	// Enabled starts the admin interface. Disabled by default.
	Enabled bool `toml:"enabled" doc:"Start the admin interface."`
	// BindAddress is the address to listen on. Empty or "*" listens on all addresses.
	BindAddress string `toml:"bind_address" doc:"Address to listen on. Empty or \"*\" listens on all addresses."`
	// Port is the admin listener port (default 8081). It must differ from the podcast server port.
	Port int `toml:"port" doc:"Admin port (default 8081). Must differ from server.port. Do not publish it directly."`
	// Auth is the authentication mode: "proxy" or "password".
	Auth string `toml:"auth" enum:"proxy,password" doc:"\"proxy\" trusts a user header from an authenticating reverse proxy; \"password\" uses HTTP Basic authentication."`
	// TrustedProxies lists IP addresses or CIDR ranges of the reverse proxy (proxy mode).
	// Requests from any other address are rejected.
	TrustedProxies []string `toml:"trusted_proxies" doc:"Reverse proxy IP addresses or CIDR ranges (proxy mode). Keep this as narrow as possible."`
	// UserHeader is the header carrying the authenticated user name (proxy mode, default "Remote-User").
	UserHeader string `toml:"user_header" doc:"Header carrying the signed-in user from the proxy (default \"Remote-User\")."`
	// Username is the login name (password mode, default "admin").
	Username string `toml:"username" doc:"Login name for password mode (default \"admin\")."`
	// PasswordHash is a bcrypt hash of the admin password (password mode). Generate one with
	// podsync --hash-password.
	PasswordHash string `toml:"password_hash" secret:"true" doc:"bcrypt hash of the admin password, from podsync --hash-password."`
}

// ApplyDefaults fills unset fields.
func (c *Config) ApplyDefaults() {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if strings.TrimSpace(c.UserHeader) == "" {
		c.UserHeader = DefaultUserHeader
	}
	if strings.TrimSpace(c.Username) == "" {
		c.Username = DefaultUsername
	}
}

// Validate checks an enabled admin configuration.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	at := func(err error, key string) error { return configschema.NewFieldError(err, "admin", key) }
	if c.Port < 1 || c.Port > 65535 {
		return at(errors.Errorf("admin.port %d is not a valid port", c.Port), "port")
	}
	switch c.Auth {
	case AuthProxy:
		if len(c.TrustedProxies) == 0 {
			return at(errors.New(`admin.trusted_proxies is required when admin.auth = "proxy"; list the reverse proxy's address or network`), "trusted_proxies")
		}
		if _, err := parseTrustedProxies(c.TrustedProxies); err != nil {
			return at(err, "trusted_proxies")
		}
	case AuthPassword:
		if strings.TrimSpace(c.PasswordHash) == "" {
			return at(errors.New(`admin.password_hash is required when admin.auth = "password"; generate one with podsync --hash-password`), "password_hash")
		}
		if _, err := bcrypt.Cost([]byte(c.PasswordHash)); err != nil {
			return at(errors.Wrap(err, "admin.password_hash is not a valid bcrypt hash; generate one with podsync --hash-password"), "password_hash")
		}
	case "":
		return at(errors.New(`admin.auth is required when the admin interface is enabled: "proxy" or "password"`), "auth")
	default:
		return at(errors.Errorf(`admin.auth %q must be "proxy" or "password"`, c.Auth), "auth")
	}
	return nil
}

// parseTrustedProxies parses IP addresses and CIDR ranges.
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, errors.Errorf("admin.trusted_proxies entry %q is not an IP address or CIDR range", entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, errors.Errorf("admin.trusted_proxies entry %q is not an IP address or CIDR range", entry)
		}
		networks = append(networks, network)
	}
	return networks, nil
}
