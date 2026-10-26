# Status and next steps

Handover file for the maintainer and for Claude Code sessions working on this repository. **Read this first, and update it after every step** (state, decisions, next steps), so the next session can continue without the chat history.

Last updated: 2026-10-26 (1.3.4: vacation mode)

## Current state

- **1.3.4 (2026-10-26)** on our `main`: vacation mode from branch `vacation-mode` (`1efaa13`, from `upstream/main`): `max/<id>/vacation/set` with `{"temperature": 12, "until": "YYYY-MM-DD HH:MM"}` (temperature optional, default eco; `off` ends it), SetTemperature `0x40` with the three until bytes (FHEM `10_MAX.pm`); `until` decoded into `max/<id>/state` and a "Vacation Until" sensor; in vacation mode bytes 3-5 are no longer read as actual temperature. Test `vacation_test.go` (encoding checked against the example `9D0B..` in Bouni/max-cube-protocol `S-Message.md`). `go vet`/`go test`/`go build` passed on the branch and on the merge. Not installed on the device yet, not checked with a real thermostat.
- **1.3.3 installed on the device since 2026-10-25, not started yet** (the add-on is stopped). Not checked yet: anything with a real CUL. Before the first start, `gateway_id` must be the RF address of the old MAX! Cube, so the devices need no re-pairing (guide at the top of `max2mqtt/README.md`).
- **1.3.3 (2026-10-25)** is our `main`: Dad-Ju's 1.3.2 plus three feature branches, merged with merge commits:
  - `culfw-auto-ack` (`8aa701b`): `Za<gateway_id>` on CUL init, own auto-Acks ignored. Test `autoack_test.go`.
  - `valve-position-sensor` (`1175d15`): valve position 0–100 % retained on `max/<id>/valve` (own topic, not in `max/<id>/state`), RF error binary sensor (`rf_error` in `max/<id>/state`). Test `valve_test.go`.
  - `shutter-contact` (`a7c593a`): shutter contacts (`0x30`) with window, battery and RF error binary sensors. Test `shutter_contact_test.go`.
  - `go vet`, `go test` and `go build` passed on each branch (Go 1.25 installed only for that session).
- **No pull requests opened yet.** The three branches are pushed to `dktzde/max2mqtt` and start from `upstream/main` (Dad-Ju `d1e83ab`), so each can be offered alone.

## Next steps

1. Find the Cube's RF address over the radio (`log_level: debug`, `dst=` of a thermostat or window message), set `gateway_id`, start the add-on.
2. Test with the real CUL: valve position against the thermostat display, window open/closed and battery at a shutter contact, no repeated messages after Auto-Ack, a setpoint change from Home Assistant confirmed by Ack.
3. Test vacation mode at one radiator thermostat: set a date 1-2 hours ahead, check the display (suitcase symbol, end date), the "Vacation Until" sensor, then the return to auto at the end time; also `off`.
4. Write the result here with date and time ("1.3.4 on the device since …, checked …").
5. After the test and the maintainer's go-ahead: four separate PRs to `Dad-Ju/max2mqtt` from the four branches; Auto-Ack also to `pwurbs/max2mqtt` on a new branch from `pwurbs/main` (that tree has no Ack block yet, so only `Za` plus ignoring own auto-Acks).

## Known issues and backlog

- A radiator thermostat only reports when something changes, so the valve position arrives irregularly and `0 %` may stay for hours.
- Not covered (see README): eco push buttons as entities, RSSI, Cube polling, fakeWT/fakeSC.
- Dad-Ju has issues turned off: suggestions only as pull requests.
- `aarch64` is never test-built here.

## Decisions and conventions

- **Remotes:** `origin` = `dktzde/max2mqtt`, `upstream` = `Dad-Ju/max2mqtt` (active upstream, itself a fork of pwurbs), `pwurbs` = `pwurbs/max2mqtt` (original).
- **One feature = one branch from `upstream/main` = one PR**, so upstream can take each one alone. Our `main` merges them with merge commits (not squashed).
- **Branches for upstream PRs:** no version bump, only `## Unreleased` in `max2mqtt/CHANGELOG.md`, no `STATUS.md`/`CLAUDE.md`. Commit style there: plain imperative sentences, like Dad-Ju's commits.
- **Our `main`:** sets the version in `max2mqtt/config.yaml` **and** `io.hass.version` in `max2mqtt/Dockerfile`. The add-on builds from source on the device, so whatever is on `main` with a new version reaches Home Assistant on the next update. Docs-only changes get no new version.
- **Changelog:** every version has its date as `<sub>YYYY-MM-DD</sub>` under the heading (taken from the git history for older versions). A line `---` and a bold note separate the versions by Dad-Ju (1.2.0–1.3.2) and the original by pwurbs (1.0.0–1.1.0) from this fork (1.3.3 and later). The READMEs list the history by author in the same way.
- **Changes to `main` go through a pull request** in this repository, also docs (`gh pr create --repo dktzde/max2mqtt --base main`, otherwise `gh` targets the upstream).
- **Language:** code, docs, changelog, commits and PR texts in English. Replies to the maintainer in German, in plain language.
- No private data (IP addresses, real RF addresses) in this public repository; examples use placeholders such as `0A1B2C`.

- **Protocol references:** FHEM `10_MAX.pm` (device messages, `desiredTemperature … until`) and `14_CUL_MAX.pm` (CUL side) from [fhem/fhem-mirror](https://github.com/fhem/fhem-mirror) (`fhem/FHEM/`); Cube LAN protocol incl. byte layouts in [Bouni/max-cube-protocol](https://github.com/Bouni/max-cube-protocol). Check there before guessing a message layout.

## Key files

- `max2mqtt/config.yaml`, `max2mqtt/Dockerfile`: add-on version (both), options, build
- `max2mqtt/CHANGELOG.md`: shown by Home Assistant in the update dialog
- `max2mqtt/main.go`: CUL init (`initializeCUL`), message handling, decoding, MQTT discovery
- `max2mqtt/shutter_contact.go`: shutter contacts
- `max2mqtt/vacation.go`: vacation mode (command, until encoding/decoding)
- `max2mqtt/*_test.go`: unit tests (`go test ./...` in `max2mqtt/`)
- `README.md`, `max2mqtt/README.md`, `max2mqtt/DOCS.md`: repository page, add-on overview, Documentation tab
- `repository.yaml`: add-on store entry of this fork
