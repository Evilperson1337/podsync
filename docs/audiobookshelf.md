# Audiobookshelf Hardlink Export

Podsync can place each finalized episode into an [Audiobookshelf](https://www.audiobookshelf.org/) podcast library as a **hardlink**. Audiobookshelf sees an ordinary media file in the podcast directory, while both paths share the same bytes on disk, so no extra storage is used.

Podsync stays the downloader and owns the source file. It does not call the Audiobookshelf API, write to its database, or copy media.

## Requirements

- **Local storage only.** Hardlinks cannot be created from S3 storage. Enabling export with `storage.type = "s3"` fails at startup.
- **Same filesystem.** The Podsync data directory and the Audiobookshelf podcast root must be on the same device. When they are not, the export fails with a `cross_device` error. Podsync does not fall back to copying.
- **An existing Audiobookshelf podcast.** The per-feed `directory` should be the folder of a podcast that already exists in your Audiobookshelf library. Podsync creates the folder if it is missing, but Audiobookshelf only adopts files in folders that belong to one of its podcasts.

## Configuration

Export is off by default. A feed exports only when **both** the global section and the feed section enable it.

```toml
[storage]
type = "local"
  [storage.local]
  data_dir = "/data/podsync"

[audiobookshelf]
enabled = true
podcast_root = "/data/media/podcasts"

[feeds]
  [feeds.doctrine]
  url = "https://www.youtube.com/@example"
  format = "audio"

  [feeds.doctrine.audiobookshelf]
  enabled = true
  directory = "Doctrine"
```

| Key | Description |
| --- | --- |
| `audiobookshelf.enabled` | Enables export globally. Defaults to `false`. |
| `audiobookshelf.podcast_root` | Audiobookshelf podcast library root. Required when enabled. It must already exist; Podsync never creates it. |
| `feeds.<id>.audiobookshelf.enabled` | Enables export for this feed. Defaults to `false`. |
| `feeds.<id>.audiobookshelf.directory` | Podcast directory **relative** to `podcast_root`. Required when the feed is enabled. Absolute paths and `..` segments that escape `podcast_root` are rejected at startup. |

The episode keeps the filename Podsync already uses, for example:

```text
/data/podsync/doctrine/5J3pSc6nJl8.mp3
→ /data/media/podcasts/Doctrine/5J3pSc6nJl8.mp3
```

## Docker / Unraid path mapping

Mount the **shared parent** directory once, so both paths are on the same mount inside the container. The kernel refuses hardlinks across mount points, so two separate bind mounts fail with `cross_device` even when they point at folders on the same disk.

| Host path | Container path | Mode |
| --- | --- | --- |
| `/mnt/user/data` | `/data` | Read/Write |

Host layout:

```text
/mnt/user/data/
├── podsync/            → storage.local.data_dir = "/data/podsync"
│   └── doctrine/
└── media/
    └── podcasts/       → audiobookshelf.podcast_root = "/data/media/podcasts"
        └── Doctrine/
```

Audiobookshelf can keep its own mapping of the podcast library (for example `/mnt/user/data/media/podcasts` → `/podcasts`); only the Podsync container needs the shared parent.

## Lifecycle

```text
download → processing / trimming → publish into Podsync storage
→ Audiobookshelf hardlink → post_episode_download hooks → episode stored
```

- Only the final published file is linked, never temporary or partial media.
- Podsync records each link's device and inode in its database, so it can later tell its own links apart from other files.
- After cleanup, each feed run checks every retained episode. It creates any missing links (including for episodes downloaded before export was enabled), and it propagates deletions in both directions (see [Mirroring](#mirroring)).
- Media is not modified after it is linked. Embedded metadata written during processing is visible to Audiobookshelf as-is.

## Failure handling

Audiobookshelf is a secondary target. An export failure never fails the Podsync download, never changes episode state, and never triggers a re-download. The next feed run retries it.

| Status | Meaning | Log level |
| --- | --- | --- |
| `linked` | New hardlink created | info |
| `already_linked` | Destination is already the same inode | debug |
| `conflict` | Destination exists and is a different file; it is left untouched | warning |
| `cross_device` | Source and destination are on different filesystems; includes device IDs | warning |
| `failed` | Other errors, such as missing source, missing `podcast_root`, or permissions | warning |
| `removed` | The Audiobookshelf hardlink was deleted to mirror Podsync | info |
| `absent` | No Audiobookshelf file to remove | debug |
| `deleted_in_library` | The link was deleted in Audiobookshelf; the Podsync copy is removed too | info |
| `link_elsewhere` | The recorded link is missing from its path, but the file still has other links (moved, renamed, or library not mounted). Nothing is changed | warning |
| `unverified` | The Podsync file is gone, and the Audiobookshelf file does not match a recorded link, so it is left in place | warning |

Log entries include `feed_id`, `episode_id`, `title`, `source_path`, `destination_path`, `status`, and, where available, `inode`, `links`, `source_device`, and `destination_device`.

When `server.debug_endpoints` is enabled, `/debug/vars` exposes `audiobookshelf_links_created_total`, `audiobookshelf_links_removed_total`, `audiobookshelf_mirrored_deletions_total` and `audiobookshelf_export_failures_total`.

## Mirroring

The Audiobookshelf podcast directory mirrors Podsync for every feed with export enabled. A deletion on either side is applied to the other.

| Event | Result |
| --- | --- |
| Podsync cleanup (`clean.keep_last`) ages out an episode | The Audiobookshelf link is deleted first, then the Podsync file. The episode is marked `cleaned`. |
| A Podsync file is deleted by hand | On the next run, the Audiobookshelf link is deleted and the episode is marked `cleaned`. |
| An episode file is deleted in Audiobookshelf | On the next run, the Podsync file is deleted and the episode is marked `cleaned`. It is dropped from the RSS feed and not downloaded again. |

Safety rules. Podsync only deletes files it can prove are its own:

- **Same inode only.** An Audiobookshelf file is deleted only when it is the same inode as the Podsync file. If the Podsync file is already gone, it must instead match the device and inode Podsync recorded when it created the link. A different file with the same name is never deleted.
- **Link count check.** A Podsync file is deleted after an Audiobookshelf deletion only when its link count has dropped to 1, which means the Audiobookshelf link no longer exists anywhere. If the library is not mounted, or the podcast folder was renamed or moved, the link still exists, the count stays at 2, and nothing is deleted (`link_elsewhere`).
- **Library must be reachable.** No deletions happen in either direction while `podcast_root` is inaccessible.
- **Keep both on failure.** If cleanup cannot delete the Audiobookshelf file (for example, a permissions error), the Podsync copy is kept too, and cleanup retries on the next run.
- **Changing `directory`.** Links recorded under the old directory are no longer tracked. Episodes are linked into the new directory, and old links are left for you to remove.

Limitations:

- Mirroring deletions requires device and inode numbers, which are only available on Linux/Unix. On Windows, links are created and cleanup still removes them, but deletions made outside Podsync are not mirrored.
- Links that existed before this feature recorded them (for example, created by hand) are recorded the first time they are seen with the Podsync file present. If the Podsync file is deleted by hand before that happens, the Audiobookshelf copy is left in place (`unverified`).
- If Audiobookshelf rewrites a file in place (for example, by embedding metadata), it becomes a different inode and is treated as a `conflict`. It is never deleted by Podsync.

## Production validation

1. Enable export for one feed (for example `doctrine`) and restart Podsync. Confirm the startup log shows `audiobookshelf hardlink export enabled`.
2. Wait for a feed run, or run once with `--headless`. Look for `Linked Video Name: ... into Audiobookshelf` entries. Existing episodes are backfilled on the first run.
3. On the host, compare the two paths:

   ```bash
   stat -c 'device=%d inode=%i links=%h path=%n' \
     /mnt/user/data/podsync/doctrine/<episode>.mp3 \
     '/mnt/user/data/media/podcasts/Doctrine/<episode>.mp3'
   ```

   Expect the same `device`, the same `inode`, and `links` of 2 or more.
4. Confirm Audiobookshelf lists the episode without a manual scan and plays it.
5. Trigger another feed run. There should be no new files in the podcast directory, and no `linked` log entries for episodes that were already linked (enable `--debug` to see `already_linked`).
6. Optional: to check mirroring, delete one episode in Audiobookshelf (with "delete from file system"), trigger a feed run, and look for `was deleted in Audiobookshelf; removed it from Podsync`. The Podsync file should be gone and the episode dropped from the RSS feed. To check cleanup, set `clean = { keep_last = N }` on the feed so an episode is aged out. After the next run, look for `Removed Video Name: ... from Audiobookshelf`, and check that the episode is gone from both `/mnt/user/data/podsync/doctrine/` and `/mnt/user/data/media/podcasts/Doctrine/`. Audiobookshelf may need a library scan before the episode disappears from its UI.

## Troubleshooting

- **`cross_device`**: the two paths are on different mounts inside the container. Mount their shared parent as a single volume. On Unraid, also check that both folders live on the same pool or array.
- **`podcast_root ... is not accessible`**: the Audiobookshelf library path is not mounted into the Podsync container.
- **`conflict`**: a different file with the same name already exists in the Audiobookshelf directory. Remove or rename it and the next run will link the Podsync file.
- **`link_elsewhere`**: the podcast folder was renamed or moved in Audiobookshelf. Update `directory` in the feed config to match.
