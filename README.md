# Podsync

![Podsync](docs/img/logo.png)

[![](https://github.com/mxpv/podsync/workflows/CI/badge.svg)](https://github.com/mxpv/podsync/actions?query=workflow%3ACI)
[![Nightly](https://github.com/mxpv/podsync/actions/workflows/nightly.yml/badge.svg)](https://github.com/mxpv/podsync/actions/workflows/nightly.yml)
[![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/mxpv/podsync)](https://github.com/mxpv/podsync/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/mxpv/podsync)](https://goreportcard.com/report/github.com/mxpv/podsync)
[![GitHub Sponsors](https://img.shields.io/github/sponsors/mxpv)](https://github.com/sponsors/mxpv)
[![Patreon](https://img.shields.io/badge/support-patreon-E6461A.svg)](https://www.patreon.com/podsync)

Podsync - is a simple, free service that lets you listen to any YouTube / Vimeo channels, playlists or user videos in
podcast format.

Podcast applications have a rich functionality for content delivery - automatic download of new episodes,
remembering last played position, sync between devices and offline listening. This functionality is not available
on YouTube and Vimeo. So the aim of Podsync is to make your life easier and enable you to view/listen to content on
any device in podcast client.

## ✨ Features

- Works with YouTube, Vimeo, SoundCloud, Twitch, and Rumble.
- Supports feeds configuration: video/audio, high/low quality, max video height, etc.
- mp3 encoding
- Update scheduler supports cron expressions
- Concurrent feed updates with per-feed deduplication and bounded execution.
- Provider-scoped execution limiting for expensive integrations such as Rumble.
- Episodes filtering (match by title, duration).
- Feeds customizations (custom artwork, category, language, etc).
- OPML export.
- Supports episodes cleanup (keep last X episodes).
- Configurable hooks for custom integrations and workflows.
- One-click deployment for AWS.
- Runs on Windows, Mac OS, Linux, and Docker.
- Supports ARM.
- Automatic yt-dlp self update.
- Supports API keys rotation.

## 📋 Dependencies

If you're running the CLI as binary (e.g. not via Docker), you need to make sure that dependencies are available on
your system. Currently, Podsync depends on `yt-dlp` ,  `ffmpeg`, and `go`.

If you enable audio-signature trimming or SponsorBlock-based media processing, `ffprobe` must also be available on `PATH`.

On Mac you can install those with `brew`:
```
brew install yt-dlp ffmpeg go
```

## 📖 Documentation

- [How to get Vimeo API token](./docs/how_to_get_vimeo_token.md)
- [How to get YouTube API Key](./docs/how_to_get_youtube_api_key.md)
- [Podsync on QNAP NAS Guide](./docs/how_to_setup_podsync_on_qnap_nas.md)
- [Schedule updates with cron](./docs/cron.md)
- [Audio signature detection](./docs/audio_signature_detection.md)
- [Audio signature examples (Windows)](./docs/audio_signature_examples.md)
- [Runtime architecture](./docs/runtime_architecture.md)
- [Observability and operations](./docs/observability.md)

## 🌙 Nightly builds

Nightly builds uploaded every midnight from the `main` branch and available for testing:

```bash
$ docker run -it --rm ghcr.io/mxpv/podsync:nightly
```

### 🔑 Access tokens

In order to query YouTube or Vimeo API you have to obtain an API token first.

- [How to get YouTube API key](https://elfsight.com/blog/2016/12/how-to-get-youtube-api-key-tutorial/)
- [Generate an access token for Vimeo](https://developer.vimeo.com/api/guides/start#generate-access-token)

## ⚙️ Configuration

You need to create a configuration file (for instance `config.toml`) and specify the list of feeds that you're going to host.
See [config.toml.example](./config.toml.example) for all possible configuration keys available in Podsync.

Minimal configuration would look like this:

```toml
[server]
port = 8080

[storage]
  [storage.local]
  # Don't change if you run podsync via docker
  data_dir = "/app/data/"

[tokens]
youtube = "PASTE YOUR API KEY HERE" # See config.toml.example for environment variables

[feeds]
    [feeds.ID1]
    url = "https://www.youtube.com/channel/UCxC5Ls6DwqV0e-CYcAKkExQ"

    [feeds.RUMBLE_VIDEOS]
    url = "https://rumble.com/c/DrDisrespect"

    [feeds.RUMBLE_LIVE]
    url = "https://rumble.com/c/StevenCrowder/livestreams"
```

If you want to hide Podsync behind reverse proxy like nginx, you can use `hostname` field:

```toml
[server]
port = 8080
hostname = "https://my.test.host:4443"

[feeds]
  [feeds.ID1]
  ...
```

Server will be accessible from `http://localhost:8080`, but episode links will point to `https://my.test.host:4443/ID1/...`

### Update execution model

Podsync now runs feed updates with bounded concurrency. Different feeds may run in parallel, while the same feed is deduplicated if it is already queued or in-flight. Runtime queue and active-update counters are exported through [`/debug/vars`](services/web/server.go) when debug endpoints are enabled.

For providers with stricter operational characteristics, Podsync can also apply provider-scoped execution limits. The current runtime limits Rumble feed execution more conservatively than the general worker pool so scraping-heavy jobs do not overwhelm the system.

Each scheduled feed run also carries a durable `execution_id` through the scheduler and updater logs, making it easier to trace one feed run end-to-end.

### Episode lifecycle and failures

Episodes now move through richer persisted states such as `planned`, `downloading`, `processing`, `stored`, and `published`. Failures persist retry metadata including last error, timestamp, retry count, and failure category. The [`/health`](services/web/server.go) endpoint reports recent failures by category.

If Podsync encounters interrupted work on startup or before a new run, incomplete transient states are reconciled back into explicit retryable error state so operators can reason about recovery more directly.

### Hook execution

Hook commands are now platform-aware. Multi-argument commands execute directly. Single-string commands can use explicit shell selection with `shell = "cmd"`, `shell = "powershell"`, `shell = "pwsh"`, or `shell = "sh"`. Shell-like single-string commands without an explicit shell use the platform default.

### Signature configuration

Optional signature trimming root can now be configured explicitly:

```toml
[signatures]
root_dir = "/app/data"
```

When signature-related features are enabled, Podsync validates `ffmpeg` and `ffprobe` availability during startup.

### Storage publication semantics

Local storage writes are now staged into sibling temporary files and atomically renamed into place. S3 publication uses a staged upload followed by publish copy, making backend-specific publication behavior explicit and reducing partially visible output risk.

Publication activity is also persisted through summary metadata so XML/OPML build counts and last publication timestamps survive restarts.

Podsync now also uses an explicit staged publish helper above [`fs.Storage`](pkg/fs/storage.go) for media and publication artifacts. Content is staged, minimum-size validated where appropriate, and only then committed to the underlying backend.

### 🌍 Environment Variables

Podsync supports the following environment variables for configuration and API keys:

| Variable Name                | Description                                                                               | Example Value(s)                              |
|------------------------------|-------------------------------------------------------------------------------------------|-----------------------------------------------|
| `PODSYNC_CONFIG_PATH`        | Path to the configuration file (overrides `--config` CLI flag)                            | `/app/config.toml`                            |
| `PODSYNC_YOUTUBE_API_KEY`    | YouTube API key(s), space-separated for rotation                                          | `key1` or `key1 key2 key3` |
| `PODSYNC_VIMEO_API_KEY`      | Vimeo API key(s), space-separated for rotation                                            | `key1` or `key1 key2`        |
| `PODSYNC_SOUNDCLOUD_API_KEY` | SoundCloud API key(s), space-separated for rotation                                       | `soundcloud_key1 soundcloud_key2`             |
| `PODSYNC_TWITCH_API_KEY`     | Twitch API credentials in the format `CLIENT_ID:CLIENT_SECRET`, space-separated for multi | `id1:secret1 id2:secret2`                     |

## 🚀 Getting started

### What to expect at startup

When Podsync starts successfully in Docker, the first log lines usually only show process startup, dependency checks, database open, and the HTTP listener binding. A log like [`running listener at :8080`](cmd/podsync/main.go:258) means the service is up and waiting for scheduled work.

Feed discovery does **not** necessarily produce an immediate burst of download logs at the exact moment the container starts. What happens next depends on your feed configuration:

- If a feed has `cron_schedule` configured, Podsync registers that schedule and waits for it.
- If a feed does **not** have `cron_schedule`, Podsync falls back to `update_period`.
- The default `update_period` is 6 hours in [`pkg/model/defaults.go`](pkg/model/defaults.go:11).
- In the Docker image, Podsync starts with [`--no-banner`](Dockerfile:30), and that path enqueues an initial update on startup in addition to scheduling future runs; this behavior is implemented in [`cmd/podsync/main.go`](cmd/podsync/main.go:230).

If you do not see any update activity after startup, the most common causes are:

- the container is not actually using the `config.toml` file you think it is,
- the config contains no active feeds,
- provider API credentials are missing,
- or your feeds are configured with a schedule you are not expecting.

### 1. Create a minimal configuration

You need a [`config.toml`](README.md) file that defines:

- a web server port,
- local storage,
- API tokens if required by the provider,
- and at least one feed under `[feeds]`.

Minimal example:

```toml
[server]
port = 8080

[storage]
  type = "local"

  [storage.local]
  data_dir = "/app/data"

[tokens]
youtube = "PASTE YOUR API KEY HERE"

[feeds]
  [feeds.example]
  url = "https://www.youtube.com/channel/UCxC5Ls6DwqV0e-CYcAKkExQ"
  page_size = 5
  update_period = "30m"
```

Notes:

- `update_period = "30m"` is useful for testing because it is easy to reason about.
- If you use `cron_schedule`, Podsync expects standard cron syntax as documented in [`docs/cron.md`](docs/cron.md).
- The sample file in [`bin/config.toml`](bin/config.toml) is a fuller example, but many of its feeds use explicit cron schedules, which can make startup look idle if you expect immediate downloads.

### 2. Run with Docker


#### Build and run as binary:

Make sure you have created the file `config.toml`. Also note the location of the `data_dir`. Depending on the operating system, you may have to choose a different location since `/app/data` might be not writable.

```
$ git clone https://github.com/mxpv/podsync
$ cd podsync
$ make
$ ./bin/podsync --config config.toml
```

#### 🐛 How to debug

Use the editor [Visual Studio Code](https://code.visualstudio.com/) and install the official [Go](https://marketplace.visualstudio.com/items?itemName=golang.go) extension. Afterwards you can execute "Run & Debug" ▶︎ "Debug Podsync" to debug the application. The required configuration is already prepared (see `.vscode/launch.json`).


#### 🐳 Run via Docker:

```
$ docker pull ghcr.io/mxpv/podsync:latest
$ docker run \
    -p 8080:8080 \
    -v $(pwd)/data:/app/data/ \
    -v $(pwd)/db:/app/db/ \
    -v $(pwd)/config.toml:/app/config.toml \
    ghcr.io/mxpv/podsync:latest
```

On Windows PowerShell, the same command usually looks like:

```powershell
docker run `
  -p 8080:8080 `
  -v ${PWD}/data:/app/data/ `
  -v ${PWD}/db:/app/db/ `
  -v ${PWD}/config.toml:/app/config.toml `
  ghcr.io/mxpv/podsync:latest
```

Important details:

- The container process looks for the config file at `/app/config.toml` by default via [`--config`](cmd/podsync/main.go:31).
- If you mount your config somewhere else, set `PODSYNC_CONFIG_PATH` or pass `--config /path/to/config.toml`.
- Persist both `/app/data` and `/app/db` so Podsync can keep downloaded media and metadata between restarts.

#### 🐳 Run via Docker Compose:

```
$ cat docker-compose.yml
services:
  podsync:
    image: ghcr.io/mxpv/podsync
    container_name: podsync
    volumes:
      - ./data:/app/data/
      - ./db:/app/db/
      - ./config.toml:/app/config.toml
    ports:
      - 8080:8080

$ docker compose up
```

### 3. Verify that the config is actually loaded

If the container starts but no feeds seem to run, first verify that your config is mounted where Podsync expects it:

- default config path: `/app/config.toml`
- default Docker working directory: `/app`
- default container command: [`/app/podsync --no-banner`](Dockerfile:29)

If you are unsure, run the container with an explicit config path:

```bash
docker run \
  -p 8080:8080 \
  -v $(pwd)/data:/app/data/ \
  -v $(pwd)/db:/app/db/ \
  -v $(pwd)/config.toml:/app/config.toml \
  ghcr.io/mxpv/podsync:latest \
  --no-banner --config /app/config.toml --debug
```

The [`--debug`](cmd/podsync/main.go:33) flag is helpful because feed scheduling and queueing logs are emitted at debug level in [`cmd/podsync/main.go`](cmd/podsync/main.go:211).

### 4. Understand why feeds may look idle

Podsync schedules feeds in [`cmd/podsync/main.go`](cmd/podsync/main.go:203):

- `cron_schedule` takes precedence when present.
- otherwise Podsync converts `update_period` into an `@every ...` schedule.

Examples:

```toml
[feeds]
  [feeds.fast_test]
  url = "https://www.youtube.com/@LinusTechTips/videos"
  update_period = "15m"
```

```toml
[feeds]
  [feeds.daily_job]
  url = "https://www.youtube.com/@LinusTechTips/videos"
  cron_schedule = "30 12 * * *"
```

For first-run testing, prefer a short `update_period` and avoid a daily `cron_schedule` until you confirm everything is working.

### 5. Quick troubleshooting checklist

If startup logs stop after `running listener at :8080`, check the following:

1. **Config mount**: make sure your host file is really mounted to `/app/config.toml`.
2. **Feeds exist**: confirm your file has at least one section under `[feeds]`.
3. **Tokens exist**: YouTube and Vimeo feeds generally require API credentials under `[tokens]` or environment variables documented in [`README.md`](README.md:158).
4. **Schedule choice**: if you set `cron_schedule`, Podsync may be waiting for the next cron tick.
5. **Use debug logs**: start with `--debug` to see schedule registration and queue activity.
6. **Check persisted output**: successful runs should populate `/app/data` and metadata under `/app/db`.

### 6. First-run recommendation

For the easiest first test:

- use one feed,
- set `update_period = "15m"` or `"30m"`,
- enable debug logging,
- mount [`config.toml`](README.md), [`data`](data), and [`db`](db) explicitly,
- and confirm that the feed URL and API key are valid before adding more feeds.

## 📦 How to make a release

Just push a git tag. CI will do the rest.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
