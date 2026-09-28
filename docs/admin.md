# Admin Interface

Podsync includes an optional admin interface: a dashboard showing every feed's schedule, last run, errors, episode counts and Audiobookshelf export, plus a reference of every configuration option.

It runs on **its own port**, separate from the podcast server. Podcast apps need unauthenticated access to feeds and episodes, while the admin interface must never be public; keeping them on different listeners lets a reverse proxy protect one without affecting the other.

The admin interface is read-only for now. Editing the configuration from it is planned; until then, edit the configuration file and Podsync [reloads it automatically](../README.md#reloading-the-configuration).

## Enable it

```toml
[admin]
enabled = true
port = 8081                          # must differ from [server] port
auth = "proxy"                       # or "password"
trusted_proxies = ["172.18.0.10"]    # proxy mode: the reverse proxy's address or network
user_header = "Remote-User"          # proxy mode: header carrying the signed-in user
```

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `false` | Start the admin interface. |
| `bind_address` | all addresses | Address to listen on. |
| `port` | `8081` | Admin port. Must differ from `server.port`. |
| `auth` | (required) | `proxy` or `password`. |
| `trusted_proxies` | (required for `proxy`) | IP addresses or CIDR ranges allowed to connect. |
| `user_header` | `Remote-User` | Header with the authenticated user name (`proxy`). |
| `username` | `admin` | Login name (`password`). |
| `password_hash` | (required for `password`) | bcrypt hash from `podsync --hash-password`. |

Changes to `[admin]` take effect after a restart.

## Authentication modes

### `proxy`: sign in through your reverse proxy (recommended)

Your reverse proxy authenticates the user (for example SWAG with Authelia or Authentik, or oauth2-proxy in front of Keycloak) and forwards the user name in a header. Podsync accepts a request only when:

1. it comes **directly** from an address in `trusted_proxies` (the TCP peer; `X-Forwarded-For` is never trusted), and
2. it carries a non-empty `user_header`.

Anything else is rejected (`403` for an untrusted address, `401` without a user). Rejections are logged with the peer address, which helps when setting `trusted_proxies`.

**Security requirements for proxy mode:**

- **Never route the admin port through the proxy without authentication.** The proxy must set `user_header` itself, overwriting any value the client sent. The auth includes shown below do this; a location without them would let a client send its own `Remote-User`.
- **Do not publish the admin port** on the host (`ports:` in Docker). Only the proxy should reach it.
- **Keep `trusted_proxies` narrow.** Any container that can reach the admin port from a trusted address can claim to be any user. Trust the proxy's own address (give it a fixed IP) or a network that only the proxy and Podsync share, rather than a broad Docker network.
- Loopback counts too: `localhost` may connect over IPv6 (`::1`), so list `::1` as well as `127.0.0.1` if you test locally.

### `password`: HTTP Basic authentication

For setups without single sign-on, or for direct access on a trusted LAN:

```bash
podsync --hash-password            # prompts twice, prints the hash
echo 'my long passphrase' | podsync --hash-password   # scripted
docker run --rm -i <your podsync image> --hash-password   # in Docker
```

```toml
[admin]
enabled = true
auth = "password"
password_hash = "$2a$10$..."
```

Passwords must be at least 12 characters. After 10 failed attempts from one address within 5 minutes, that address is locked out for the rest of the window. Basic authentication sends the password with every request, so use it only over HTTPS (for example behind SWAG) or on a trusted network.

## SWAG example

Route a separate hostname, such as `podsync-admin.example.com`, to the admin port, and keep your existing public hostname pointed at the podcast port (8080).

`/config/nginx/proxy-confs/podsync-admin.subdomain.conf`:

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name podsync-admin.*;

    include /config/nginx/ssl.conf;

    # Pick ONE authentication provider and enable its server include.
    include /config/nginx/authelia-server.conf;
    #include /config/nginx/authentik-server.conf;

    location / {
        # ...and the matching location include. Required: it authenticates the request and
        # sets the user header that Podsync trusts.
        include /config/nginx/authelia-location.conf;
        #include /config/nginx/authentik-location.conf;

        include /config/nginx/proxy.conf;
        include /config/nginx/resolver.conf;
        set $upstream_app podsync;
        set $upstream_port 8081;
        set $upstream_proto http;
        proxy_pass $upstream_proto://$upstream_app:$upstream_port;
    }
}
```

Set `user_header` to the header your provider's include sets. SWAG's Authelia include sets `Remote-User` (the default); its Authentik include sets `X-authentik-username`. Check the include files in `/config/nginx/` for your SWAG version.

### Keycloak

SWAG has no built-in Keycloak include. Run [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) with the Keycloak OIDC provider and `--set-xauthrequest`, point an nginx `auth_request` at it, and forward its user header:

```nginx
auth_request_set $user $upstream_http_x_auth_request_preferred_username;
proxy_set_header X-Auth-Request-Preferred-Username $user;
```

```toml
user_header = "X-Auth-Request-Preferred-Username"
```

(`X-Auth-Request-User` also works if you prefer the Keycloak user ID over the user name.)

## API

The dashboard is backed by a small JSON API on the admin port (all endpoints require authentication):

| Endpoint | Description |
| --- | --- |
| `GET /api/me` | The signed-in user, auth mode and Podsync version. |
| `GET /api/status` | Every configured feed with schedule, next run, last success/failure, episode counts by state and Audiobookshelf link count. |
| `GET /api/schema` | JSON Schema of the configuration, generated from Podsync's configuration types. Secret values are marked `x-secret`. |

State-changing requests (added with configuration editing) must include an `X-Podsync-Admin` header and come from the same origin.
