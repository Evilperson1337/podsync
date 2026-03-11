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

## 🚀 How to run


### Build and run as binary:

Make sure you have created the file `config.toml`. Also note the location of the `data_dir`. Depending on the operating system, you may have to choose a different location since `/app/data` might be not writable.

```
$ git clone https://github.com/mxpv/podsync
$ cd podsync
$ make
$ ./bin/podsync --config config.toml
```

### 🐛 How to debug

Use the editor [Visual Studio Code](https://code.visualstudio.com/) and install the official [Go](https://marketplace.visualstudio.com/items?itemName=golang.go) extension. Afterwards you can execute "Run & Debug" ▶︎ "Debug Podsync" to debug the application. The required configuration is already prepared (see `.vscode/launch.json`).


### 🐳 Run via Docker:

```
$ docker pull ghcr.io/mxpv/podsync:latest
$ docker run \
    -p 8080:8080 \
    -v $(pwd)/data:/app/data/ \
    -v $(pwd)/db:/app/db/ \
    -v $(pwd)/config.toml:/app/config.toml \
    ghcr.io/mxpv/podsync:latest
```

### 🐳 Run via Docker Compose:

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

## 📦 How to make a release

Just push a git tag. CI will do the rest.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
