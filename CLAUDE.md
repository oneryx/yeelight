# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Public project (`github.com/oneryx/yeelight`, image `ghcr.io/oneryx/yeelight`): control a Yeelight bulb over the LAN
from a web page, served either by a Docker web server or by a native desktop app built with MyGo
(`github.com/egoist/mygo`, run as `go tool mygo`).

## Commands

```sh
task               # desktop app in dev mode with live reload (go tool mygo dev)
task serve         # web server on :10000, page read from web/static on disk (-dev)
task test          # go test ./...  (against a fake bulb, no real light needed)
task vet
task build         # desktop app for this machine into build/; build-all for every platform
task install       # macOS: build and install into /Applications
task docker-build  # local image build; runs the tests inside the build
task icon          # regenerate icons from assets/icon.svg (uses macOS sips)
go test ./web -run TestName   # single test
```

## Architecture

- `light/yeelight.go`: the Yeelight LAN protocol (JSON lines over TCP, port 55443) and SSDP discovery.
- `light/controller.go`: `Controller` owns all bulb I/O on one goroutine. `Send(kind, fn, overrides...)` queues
  commands keyed by kind; a newer command of the same kind replaces an unsent one (keeps slider drags from flooding
  the bulb, which rate-limits). Rounds are throttled (200ms), state is polled every 5s when idle, and results go to a
  `report(Props, error)` callback. A discovered host is kept and reported via `OnDiscover`.
- `web/web.go`: `web.Server` wraps the controller, holds a versioned `State` (props, connection error, address,
  schemes) and serves the embedded page plus the JSON API. Commands answer 202 immediately; outcomes only show in
  the state. Clients long-poll `GET /api/state?wait=<version>` (25s), woken by closing the `changed` channel in
  `notify()`. Temperature and color share the command kind `"color"` so the last one wins.
- Two entrypoints run the same `web.Server`/`Handler()`:
  - `cmd/server`: Docker web server on :10000; persists to `$YEELIGHT_DATA/{address,schemes.json}`;
    `-healthcheck` flag for the scratch image's HEALTHCHECK.
  - `main.go`: desktop app; serves the handler through MyGo's `mygo://localhost/` protocol, persists in the MyGo
    user data dir (`host`, `schemes.json`). Under `mygo dev` it reads `web/static` from disk.
  - `YEELIGHT_HOST` is the default address for both until one is saved from the page. Keep it: SSDP discovery
    doesn't work in bridged Docker.
- `web/static`: plain JS page (no build step). Use relative URLs (`api/state`, not `/api/state`) so it works under
  both `http://` and `mygo://`. `sw.js` is a stale-while-revalidate service worker for the page shell (never
  `/api/`); `app.js` caches the last state in localStorage to render before connecting. Add new shell files to
  `SHELL` in `sw.js`.

## Tests

- `light/lighttest` is a fake bulb on localhost that records the commands sent (`Sent()`); used by both `light` and
  `web` tests. `light/export_test.go` exposes `SetDiscover` so tests never multicast.
- The Docker build only runs `go test ./light/... ./web/...` (the desktop app files are excluded via
  `.dockerignore`), so server-side code must not depend on `main.go`/MyGo.

## CI / releases

- `.github/workflows/docker.yml`: push to `main` publishes `:latest` (multi-arch amd64/arm64/arm/v7); `v*` tags
  publish semver tags. Desktop-only paths (`main.go`, `mygo.json`, `assets/`, `resources/`, `tools/`, docs) don't
  trigger it.
- `.github/workflows/release.yml`: a `v*` tag builds the desktop app for all platforms into a GitHub release. The tag
  must equal `v` + `version` in `mygo.json`, so bump that first.
- The README documents the API table, env vars and usage; update it when changing endpoints or behavior.
