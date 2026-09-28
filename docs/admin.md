# Admin Interface

Podsync includes an optional admin interface: a dashboard showing every feed's schedule, last run, errors, episode counts and Audiobookshelf export, and an editor for every configuration option.

It runs on **its own port**, separate from the podcast server. Podcast apps need unauthenticated access to feeds and episodes, while the admin interface must never be public; keeping them on different listeners lets a reverse proxy protect one without affecting the other.

The configuration file stays the source of truth. The editor reads and writes that file, and you can keep editing it by hand as a fallback.

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

## Editing the configuration

The **Configuration** tab shows every option, grouped by section, with its description. Feeds, API tokens, signature rules and hooks can be added, renamed, reordered and removed.

- **Check** validates the edited configuration exactly as startup would (unknown keys, value types, cron schedules, signature files, `ffmpeg` availability) and shows a preview of the file that will be written. Nothing is written.
- **Save** validates again, writes the file and applies it immediately: feeds, tokens and other [reloadable settings](../README.md#reloading-the-configuration) take effect right away, and the result lists added, updated and removed feeds. An invalid configuration is never written.
- **Settings that need a restart** (`[server]`, `[storage]`, `[database]`, `[downloader]`, `[log]`, `[signatures]`, `[audiobookshelf]`, `[admin]`) are saved but show a banner until Podsync is restarted.

### How the file is written

The editor writes the whole file in the same format it was in (TOML, YAML or JSON), in a standard order, with each option's description as a comment. **Comments you added by hand are not kept** when the editor saves; the previous file is kept in History (see below), so nothing is lost.

Every write is parsed back and compared with what you saved before anything reaches disk. The file is replaced atomically (written to a temporary file and renamed). If the configuration file itself is a Docker single-file bind mount, renaming is not possible and the file is rewritten in place instead; mount the directory that contains the file to get atomic saves (see [Reloading the configuration](../README.md#reloading-the-configuration)).

### Hand edits and conflicts

The editor remembers which version of the file it loaded. If the file changes before you save (you edited it by hand, or another admin saved), the save is refused and nothing is overwritten; load the current file and redo your change. Hand edits are applied by the normal [file watcher](../README.md#reloading-the-configuration).

### Secrets

API tokens and the admin password hash are **write-only**. They are never sent to the browser: the editor shows "Set (hidden)", and you can replace or remove a value but not read it. Unchanged secrets are kept from the file on save.

The admin password is set with **Set password** under `[admin]`: the password is hashed on the server with bcrypt, and only the hash is written. It takes effect after a restart.

### Environment overrides

Options set by environment variables (`PODSYNC__<SECTION>__<KEY>` or `PODSYNC_<PROVIDER>_API_KEY`) are shown disabled with the variable's name: the environment wins over the file, so editing them would have no effect. Environment values are never written to the file.

### History

Every save and restore first keeps the replaced file next to the configuration as `<name>.bak.<UTC time>` (the last 10 versions, readable only by the Podsync user because they contain secrets). The **History** tab lists them; **Restore** writes a version back byte for byte, including its original comments, and applies it. Restoring also keeps the version it replaces, so a restore can itself be undone.

Every save and restore is logged with the admin user name.

## API

The dashboard is backed by a small JSON API on the admin port (all endpoints require authentication):

| Endpoint | Description |
| --- | --- |
| `GET /api/me` | The signed-in user, auth mode and Podsync version. |
| `GET /api/status` | Every configured feed with schedule, next run, last success/failure, episode counts by state and Audiobookshelf link count. |
| `GET /api/schema` | JSON Schema of the configuration, generated from Podsync's configuration types. Secret values are marked `x-secret`. |
| `GET /api/config` | The configuration file as a document, with secrets masked, its `version`, environment overrides and sections waiting for a restart. |
| `POST /api/config/validate` | Validate `{"document": ...}` without writing; returns errors, restart-only sections and a preview. |
| `PUT /api/config` | Save `{"document": ..., "version": ...}`. `409` if the file changed since `version`, `422` if invalid. |
| `GET /api/config/backups` | Saved versions, newest first. |
| `POST /api/config/backups/{name}/restore` | Restore a saved version, given the current `{"version": ...}`. |
| `POST /api/password-hash` | Hash `{"password": ...}` (12+ characters) for `admin.password_hash`. |

Masked secrets are sent as `__podsync_secret_unchanged__`; sending that value back keeps the current secret.

State-changing requests must include an `X-Podsync-Admin` header and come from the same origin.
