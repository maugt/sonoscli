# sonoscli – Design & Specification

This document describes the overall architecture, command surface, and key implementation details of `sonoscli`.

## Goals

- Discover all speakers reliably and present room names consistent with the Sonos app.
- Provide fast, scriptable playback control from the terminal.
- Be coordinator-aware so commands behave like the Sonos controller apps.
- Support Spotify enqueue/play without requiring Spotify credentials (using Sonos-linked Spotify).
- Support Sonos-side music-service search (SMAPI) when services are linked in the Sonos app.
- Optionally support Spotify search via Spotify Web API (requires credentials).
- Keep the implementation small, modern Go, and easy to extend.

Non-goals (for now):
- Full music-service browsing trees (Sonos SMAPI catalog browsing is large/complex and service-dependent).
- Full credential management (keychain/encryption, profiles) beyond the current local token store.

## High-level Architecture

```
cmd/sonos/                 # main entrypoint
internal/cli/              # Cobra commands and output formatting
internal/sonos/            # Sonos UPnP/SOAP, SSDP discovery, topology parsing
internal/spotify/          # Spotify Web API (client credentials) search helper
docs/spec.md               # this document
```

### Data flow

- **Discovery**
  - Primary: SSDP M-SEARCH → find *any* Sonos responder → query topology (`ZoneGroupTopology.GetZoneGroupState`) for full room list.
  - Fallback: local subnet scan for TCP `1400` + `device_description.xml` → then topology query.
  - Output is based on topology members, which match the Sonos app’s room list.

- **Control**
  - Commands resolve to a **group coordinator** when required (transport controls must go to the coordinator).
  - Commands call UPnP SOAP actions on port `1400` using a minimal SOAP client.

## Sonos Protocols Used

### SSDP (discovery)

- Multicast: `239.255.255.250:1900`
- Query: `M-SEARCH` for `urn:schemas-upnp-org:device:ZonePlayer:1`
- Result: device `LOCATION` pointing at `http://<ip>:1400/xml/device_description.xml`

SSDP can be unreliable on some networks (multicast blocked, flaky Wi‑Fi), so we do not depend on it for the final device list.

### UPnP SOAP (control and topology)

All calls are HTTP POST SOAP requests to `http://<speaker-ip>:1400/.../Control`.

Key services/actions:

- `ZoneGroupTopology`:
  - `GetZoneGroupState` → returns a `ZoneGroupState` XML payload which describes groups and members.

- `AVTransport`:
  - `Play`, `Pause`, `Stop`, `Next`, `Previous`
  - `SetAVTransportURI` (used for grouping join, and queue management)
  - `AddURIToQueue` (enqueue Spotify items)
  - `BecomeCoordinatorOfStandaloneGroup` (ungroup)

- `RenderingControl`:
  - `GetVolume`, `SetVolume`, `GetMute`, `SetMute` (plus group volume where supported)

## Command Surface

### Discovery

- `sonos discover` – list speakers (room name, IP, UDN)
  - `--format json` supported.

### Status

- `sonos status --name "<Room>"` (or `sonos now`) – show playback status, current URI, time, volume/mute, and parsed now-playing metadata when available (`Title/Artist/Album/AlbumArt`). On soundbar/HT devices, shows the audio input format (e.g. Dolby Digital, Dolby Atmos, DTS, PCM) via `GetZoneInfo` `HTAudioIn`.
  - `--format json` supported.

### Transport

- `sonos play|pause|stop|next|prev --name "<Room>"`

### Watch (events)

- `sonos watch --name "<Room>" [--duration 30s]`
  - Subscribes to `AVTransport` and `RenderingControl` UPnP events and prints changes as they arrive.
  - `--format json` prints one JSON object per line (stream-friendly).

### Volume / mute

- `sonos volume get|set --name "<Room>" <0-100>`
- `sonos mute get|on|off|toggle --name "<Room>"`

### Queue

- `sonos queue list --name "<Room>" [--start N] [--limit N]` (and `--format json|tsv`)
- `sonos queue play --name "<Room>" <pos>` (1-based)
- `sonos queue remove --name "<Room>" <pos>` (1-based)
- `sonos queue clear --name "<Room>"`

### Favorites

- `sonos favorites list --name "<Room>" [--start N] [--limit N]` (and `--format json|tsv`)
- `sonos favorites open --name "<Room>" --index <N>`
- `sonos favorites open --name "<Room>" "<title>"`

### Other sources

- `sonos play-uri --name "<Room>" "<uri>" [--title "..."] [--radio]`
- `sonos linein --name "<Room>" [--from "<RoomWithLineIn>"]`
- `sonos tv --name "<Room>"`

### Scenes

- `sonos scene save <name>` – capture grouping + per-room volume/mute
- `sonos scene apply <name>` – restore grouping + per-room volume/mute
- `sonos scene list` – list saved scenes (`--format json|tsv` supported)
- `sonos scene delete <name>` – delete a scene

### Spotify (no Spotify credentials required)

Spotify must already be linked in the Sonos app.

- `sonos open --name "<Room>" <spotify-uri-or-share-link>`
  - Adds to queue and starts playback.
- `sonos enqueue --name "<Room>" <spotify-uri-or-share-link>`
  - Adds to queue without playing.

Accepted Spotify refs:
- `spotify:track:<id>`, `spotify:album:<id>`, `spotify:playlist:<id>`, `spotify:show:<id>`, `spotify:episode:<id>`
- `https://open.spotify.com/...` share links

Implementation detail: we generate Sonos-compatible DIDL metadata similar to SoCo’s ShareLink logic and try common Spotify Sonos service numbers (`2311`, `3079`).

### Spotify search (requires Spotify Web API credentials)

- `sonos search spotify "<query>" [--type track|album|playlist|show|episode]`
  - Requires `SPOTIFY_CLIENT_ID` and `SPOTIFY_CLIENT_SECRET` (or `--client-id/--client-secret`).
  - Prints `spotify:<type>:<id>` URIs usable with `sonos open` / `sonos enqueue`.
  - `--open` / `--enqueue` optionally play/enqueue the selected result (`--index`).

### Sonos-side music-service search (SMAPI; no Spotify Web API credentials)

Spotify must be linked in the Sonos app. Some services also require a one-time DeviceLink/AppLink flow.

- `sonos smapi services` – list available services and auth types.
- `sonos smapi categories --service "Spotify"` – list available search categories for a service.
- `sonos auth smapi begin|complete --service "Spotify"` – link your account for SMAPI access.
- `sonos smapi search --service "Spotify" --category tracks "<query>"` – prints canonical Spotify URIs usable with `sonos open` / `sonos enqueue`.
- `sonos smapi browse --service "Spotify" --id root` – browse containers via SMAPI `getMetadata` (drill down by passing returned ids).

### Grouping

- `sonos group status` – show all groups, coordinators, and members
  - `--format json|tsv` supported.
- `sonos group join --name "<Room>" --to "<OtherRoomOrIP>"`
  - Sends `AVTransport.SetAVTransportURI` to the *joining* speaker with `x-rincon:<COORDINATOR_UUID>`.
  - Room selection supports fuzzy substring matching; ambiguous matches return suggestions.
- `sonos group unjoin --name "<Room>"`
  - Sends `AVTransport.BecomeCoordinatorOfStandaloneGroup` to the target speaker.
- `sonos group party --to "<RoomOrIP>"`
  - Joins all visible speakers to the target group.
- `sonos group dissolve --name "<Room>"`
  - Ungroups every member of the target group (leaves members first, coordinator last).
- `sonos group volume get|set --name "<Room>" <0-100>`
- `sonos group mute get|on|off|toggle --name "<Room>"`

## Coordinator Awareness

For transport-like actions (`play/pause/stop/next/prev`, queue operations, Spotify enqueue/open), the effective target should be the **group coordinator**. `sonoscli` resolves the coordinator via topology and sends commands to that device.

Grouping actions are different:
- `group join`: sent to the *joining* speaker.
- `group unjoin`: sent to the target speaker.

## Output Formats

- Human-readable output is tab/line oriented and intended for terminal use.
- `--format plain|json|tsv` controls output formatting where applicable.
- `--json` is retained as a deprecated alias for `--format json`.

## Testing Strategy

- Pure parsing and transformation logic has unit tests:
  - SSDP parsing
  - SOAP response/error parsing
  - Topology parsing (`ZoneGroupState`)
  - Spotify ref parsing and Spotify Web API search parsing
- CLI commands with external dependencies are tested using dependency injection:
  - Spotify search CLI tests stub a searcher and a Sonos enqueuer.
  - Grouping CLI tests stub a topology getter and a grouping client.

Integration tests (real speakers) are intentionally not part of CI.

## Tooling / CI

- Formatting: `gofmt`
- Lint: `golangci-lint` (configured in `.golangci.yml`)
- Tests: `go test ./...`
- CI: GitHub Actions runs format check, `go vet`, tests, and lint.

## Inspiration

SoCo (Python) is a major reference for Sonos protocol patterns and music-service mechanics:

```text
https://github.com/SoCo/SoCo
```
