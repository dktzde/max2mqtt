# Status and next steps

Handover file for the maintainer and for Claude Code sessions working on this repository. **Read this first, and update it after every step** (state, decisions, next steps), so the next session can continue without the chat history.

Last updated: 2026-10-28 (1.4.0: drop-in replacement for the MAX! Cube integration)

## Current state

- **1.4.0 (2026-10-28)** on our `main`: the bridge replaces Home Assistant's MAX! Cube integration (same climate modes, presets and attributes; only `maxcube.set_programme` becomes the MQTT topic `max/<id>/programme/set`). Branches from our `main` (they need this fork's valve/vacation code): `persist-device-data` (`/data/devices.json`), `cube-compatible-attributes` (`max/<id>/attributes`, valve based `hvac_action`), `cube-compatible-presets` (comfort/eco/Window Open/On/boost, `none` ends a boost). Migration guide in `max2mqtt/DOCS.md` ("Migrating from the MAX! Cube integration"). A smoke run of the binary against a fake CUL over TCP (no MQTT) decoded state and boost and wrote `devices.json`.
- **1.3.5 to 1.3.7 (2026-10-28)**: fixes from a review against the Cube integration, each a branch from `upstream/main` with one commit and a test, so upstream can take them one by one:
  - 1.3.5: `fix-preset-after-boost`, `fix-boost-setpoint` (boost with temperature 0, no 30 °C setpoint), `fix-zero-actual-temperature`, `fix-auto-week-program` (auto = temperature 0)
  - 1.3.6: `tx-wait-for-credits` (wait instead of drop, bulk queue for week profiles), `bridge-availability` (`max/bridge/status`, `publishDiscovery`), `duty-cycle-sensor`
  - 1.3.7: `config-unknown-values` (known flags, `config/import`, `config/set`), `week-profile-fixes` (part 1 only for > 7 points, text entity per day, Cube `programme` format); on the release branch: vacation needs a temperature while eco is unknown
  - 1.3.6 contained a built binary `max2mqtt/max2mqtt` by mistake (removed in 1.3.7, ignored in `max2mqtt/.gitignore`). Build check: `go build -o /dev/null ./...`.
  - Not fixed on purpose (maintainer's decision 2026-10-28): TimeInformation requests (`0x03`) are answered for every device heard, also foreign ones (FHEM answers only `isToMe`).
- **1.3.4 (2026-10-26)**: vacation mode (`vacation-mode`, `1efaa13`). **1.3.3 (2026-10-25)**: `culfw-auto-ack` (`8aa701b`), `valve-position-sensor` (`1175d15`), `shutter-contact` (`a7c593a`).
- **On the device:** 1.3.3 installed since 2026-10-25, **never started** (add-on stopped; options still defaults: `serial_port`, `gateway_id`, MQTT login). Nothing above is checked with a real CUL yet.
- **No pull requests to Dad-Ju or pwurbs yet.**

## Next steps

1. Update the add-on in Home Assistant to 1.4.0 (with backup), set `serial_port: tcp://<CUN>:2323`, MQTT user/password, `gateway_id` = Cube RF address (find it over the radio: `log_level: debug`, `dst=`), start it.
2. Import config and week program per device from the Cube integration's last attributes (DOCS migration guide), rename the new entities to the old Cube entity IDs.
3. Test with the real CUL: valve position vs. display, window contact, no repeated messages after Auto-Ack, setpoint confirmed by Ack, boost from HA and from the device (preset returns to none), `none` after boost, auto follows the program, duty cycle sensor, availability when the CUN is unplugged, restart keeps the mode, a week profile day (one message), vacation 1-2 hours ahead and `off`.
4. Write the result here with date and time ("1.4.0 on the device since …, checked …").
5. After the test and the maintainer's go-ahead: one PR per branch to `Dad-Ju/max2mqtt` (the `upstream/main` based ones first); Auto-Ack also to `pwurbs/max2mqtt` on a new branch from `pwurbs/main`.

## Known issues and backlog

- A radiator thermostat only reports when something changes, so the valve position arrives irregularly and `0 %` may stay for hours.
- Week profile and config values cannot be read from the devices; they are known only after an import or after the bridge sent them.
- TimeInformation for foreign devices (see above).
- Not covered (see README): eco push buttons as entities, RSSI, Cube polling, fakeWT/fakeSC, Cube "free memory slots".
- Branches based on `upstream/main` conflict with each other in `CHANGELOG.md` (all add `## Unreleased`) and partly in `main.go`; the release merges resolve that.
- Dad-Ju has issues turned off: suggestions only as pull requests.
- `aarch64` is only compile-checked here (`GOARCH=arm64 go build -o /dev/null .`).

## Decisions and conventions

- **Remotes:** `origin` = `dktzde/max2mqtt`, `upstream` = `Dad-Ju/max2mqtt` (active upstream, itself a fork of pwurbs), `pwurbs` = `pwurbs/max2mqtt` (original).
- **One feature = one branch from `upstream/main` = one PR**, so upstream can take each one alone. Features that need this fork's code (Cube compatibility) start from our `main` and say so. Our `main` merges them with merge commits (not squashed), through a `release-x.y.z` branch and a PR.
- **Branches for upstream PRs:** no version bump, only `## Unreleased` in `max2mqtt/CHANGELOG.md`, no `STATUS.md`/`CLAUDE.md`. Commit style there: plain imperative sentences, like Dad-Ju's commits.
- **Tests:** `mqtt_fake_test.go` (fake MQTT client, `resetTestState`) is the same file in every branch, so merges do not conflict; extra methods go into the branch's own test file. Run `go vet ./... && go test -race -count=3 ./...` (count > 1 finds state leaking between tests).
- **Our `main`:** sets the version in `max2mqtt/config.yaml` **and** `io.hass.version` in `max2mqtt/Dockerfile`. The add-on builds from source on the device, so whatever is on `main` with a new version reaches Home Assistant on the next update. Docs-only changes get no new version.
- **Changelog:** every version has its date as `<sub>YYYY-MM-DD</sub>` under the heading (taken from the git history for older versions). A line `---` and a bold note separate the versions by Dad-Ju (1.2.0–1.3.2) and the original by pwurbs (1.0.0–1.1.0) from this fork (1.3.3 and later). The READMEs list the history by author in the same way.
- **Changes to `main` go through a pull request** in this repository, also docs (`gh pr create --repo dktzde/max2mqtt --base main`, otherwise `gh` targets the upstream).
- **Language:** code, docs, changelog, commits and PR texts in English. Replies to the maintainer in German, in plain language.
- No private data (IP addresses, real RF addresses) in this public repository; examples use placeholders such as `0A1B2C`.
- **Protocol references:** FHEM `10_MAX.pm` (device messages, `desiredTemperature … until`) and `14_CUL_MAX.pm` (CUL side) from [fhem/fhem-mirror](https://github.com/fhem/fhem-mirror) (`fhem/FHEM/`); Cube LAN protocol incl. byte layouts in [Bouni/max-cube-protocol](https://github.com/Bouni/max-cube-protocol). Check there before guessing a message layout.
- Go is installed only for a session (`apk add go`), gone after an add-on restart of the Claude Code add-on.

## Key files

- `max2mqtt/config.yaml`, `max2mqtt/Dockerfile`: add-on version (both), options, build
- `max2mqtt/CHANGELOG.md`: shown by Home Assistant in the update dialog
- `max2mqtt/main.go`: CUL init (`initializeCUL`), message handling, decoding, climate discovery, presets
- `max2mqtt/transmission_manager.go`: duty cycle credits, normal and bulk queue, verification
- `max2mqtt/availability.go`, `duty_cycle.go`, `attributes.go`, `persist.go`: bridge status, credit sensors, Cube style attributes, `/data/devices.json`
- `max2mqtt/device_config.go`, `config_input.go`: config entities, known flags, `config/import` and `config/set`
- `max2mqtt/week_profile.go`: week profile (per-day text entities, Cube `programme` format)
- `max2mqtt/shutter_contact.go`, `vacation.go`: shutter contacts, vacation mode
- `max2mqtt/*_test.go`: unit tests (`go test ./...` in `max2mqtt/`)
- `README.md`, `max2mqtt/README.md`, `max2mqtt/DOCS.md`: repository page, add-on overview, Documentation tab
- `repository.yaml`: add-on store entry of this fork
