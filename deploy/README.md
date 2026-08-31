# Deploying a frame

Two binaries: `picture-backend` syncs from Nextcloud and renders photos to the
panel's exact geometry; `picture-frontend` maps those pre-rendered blobs and
cross-fades between them. They talk over a Unix socket.

## Setting up a new frame

1. **Flash** Raspberry Pi OS Lite (64-bit, Bookworm) and enable SSH.

2. **Copy the Tailscale key onto the SD card before first boot.** This has to
   happen first: you cannot push it over Tailscale SSH because Tailscale is not
   up yet. Everything else can be done remotely afterwards.

3. **Install** from an unpacked release tarball:

   ```
   sudo TS_AUTHKEY=tskey-auth-... ./install.sh
   ```

   This creates the `picture` user, installs both units, adds Tailscale's apt
   repository, and joins the tailnet as `tag:picture-frame`. It is idempotent,
   so re-running it on a live frame is safe.

4. **Set the server** in `/etc/picture-frame/config.toml` and restart
   `picture-backend`. This is the only setting that must exist before pairing,
   and it is usually the same for every frame in a household.

5. **Scan the QR code on the screen** with your phone, sign in to Nextcloud as
   yourself, and approve access. The frame stores the resulting app password in
   `/var/lib/picture-frame/auth.json` and starts syncing.

6. **Tag some photos** `Frame` (or whatever `tag` is set to) in the Nextcloud app.
   They appear within one sync interval. Removing the tag removes them again.

   Tagging a **whole folder** works too, and is usually what you want for an
   album. Nextcloud applies a tag to the folder object only — it is not
   inherited by the files inside — so the frame descends into every tagged
   folder itself, recursively, and shows the images it finds. Anything that is
   not an image (videos, documents) is passed over.

The SD image is identical for every frame apart from the Tailscale key;
everything that differs per frame is applied afterwards, remotely or by scanning.

## Updating

From your Mac, over Tailscale:

```
./deploy/remote-update.sh livingroom                  # latest release
./deploy/remote-update.sh livingroom --version v1.4.0
./deploy/remote-update.sh livingroom --local          # this working tree
```

Updates verify the checksum before unpacking, swap a symlink atomically,
health-check the socket, and roll back to the previous release on any failure.
Configuration and the paired credential are never touched, so an update never
forces a re-pair.

The frame downloads the release from GitHub itself — your Mac only sends the
command over Tailscale SSH — so it needs ordinary internet access, not just the
tailnet. The repository is public, so that download is anonymous and no frame
holds a GitHub credential of any kind.

## Tailnet policy

Frames must join **tagged**. Key expiry is disabled by default for tagged
devices, so a frame authenticates once and never again; untagged nodes expire and
would put every frame on a recurring re-auth chore. See `tailnet-policy.hujson`,
and note the deliberate absence of `checkPeriod` on the SSH rule — it would force
a browser re-auth and hang the non-interactive update command.

## Recovering

| Symptom | What it means | Fix |
| --- | --- | --- |
| QR code on screen | Not paired, or the app password was revoked | Scan it |
| "Tag photos with …" | Paired, nothing selected | Tag some photos |
| "Preparing photos..." | Selected but not yet rendered | Wait; HEIC is slow on first sync |
| A tagged folder shows nothing | Nothing image-shaped inside it | Check the folder actually holds photos |
| "Cannot reach …" | Server unreachable | Check `server` in the config, and the network |

The backend logs one line per sync saying what the selection matched, plus every
state change (pairing, new and dropped photos, each render, evictions). For more
— every WebDAV call with its status and timing, every frontend request, and
per-stage render timings — set `verbose = true` in the config, or
`FRAME_VERBOSE=1` in `/etc/picture-frame/env`, and restart `picture-backend`:

```
sudo journalctl -u picture-backend -f
```

Revoking the app password in Nextcloud's security settings is the supported way
to unlink a frame: it drops back to the QR screen on its own, with no shell
access needed.

## Pre-seeding a credential instead of scanning

`provision.sh` writes the credential an admin minted with
`occ user:auth-tokens:add`, skipping the QR screen. Use it for a frame being set
up on someone's behalf. Such a token has limited capabilities — fine for reading
files over WebDAV, but it fails if server-side encryption is enabled, because
decryption needs the user's login password.

## Notes

- The frame keeps showing cached photos when the network drops, and when
  Tailscale is down. Tailscale is for administration only; nothing in the photo
  path depends on it.
- Originals are never cached. Only the rendered RGB565 blobs are kept, bounded by
  `cache_budget_bytes` and evicted least-recently-shown first.
- The Pi Zero 2 W has no hardware HEVC decode, so HEIC is decoded in software.
  Steady state is invisible, but a large first import will use the CPU for a
  long while — which is why the backend runs at `Nice=10` and renders
  newest-first.
