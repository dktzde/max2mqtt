# Maintenance

Decisions, conventions, known issues and key files of this fork, for the maintainer and for Claude Code sessions. Read it before working on this repository and add what you learn.

There is no `STATUS.md`: the maintainer keeps the device state and the next steps outside this repository.

## Decisions and conventions

- **Remotes:** `origin` = `dktzde/max2mqtt`, `upstream` = `Dad-Ju/max2mqtt` (active upstream, itself a fork of pwurbs), `pwurbs` = `pwurbs/max2mqtt` (original).
- **One feature = one branch from `upstream/main` = one PR**, so upstream can take each one alone. Features that need this fork's code (Cube compatibility) start from our `main` and say so. Our `main` merges them with merge commits (not squashed), through a `release-x.y.z` branch and a PR.
- **Branch cleanup (2026-11-19):** on GitHub only `main` and `culfw-auto-ack` (source of the open PR Dad-Ju/max2mqtt#1) are left; merged branches are deleted after the merge. The fork was detached from Dad-Ju/max2mqtt on 2026-11-19, so new upstream PRs need a new fork. Branches based on `upstream/main` for possible upstream PRs are kept only in the maintainer's local clone: `bridge-availability`, `config-unknown-values`, `duty-cycle-sensor`, `fix-ack-message-counter`, `fix-auto-week-program`, `fix-boost-setpoint`, `fix-preset-after-boost`, `fix-zero-actual-temperature`, `shutter-contact`, `tx-wait-for-credits`, `vacation-mode`, `valve-position-sensor`, `week-profile-fixes`.
- **Branches for upstream PRs:** no version bump, only `## Unreleased` in `max2mqtt/CHANGELOG.md`, no `CLAUDE.md`/`MAINTENANCE.md`/`.coderabbit.yaml`. Commit style there: plain imperative sentences, like Dad-Ju's commits.
- **Tests:** `mqtt_fake_test.go` (fake MQTT client, `resetTestState`) is the same file in every branch, so merges do not conflict; extra methods go into the branch's own test file. Run `go vet ./... && go test -race -count=3 ./...` (count > 1 finds state leaking between tests).
- **Our `main`:** sets the version in `max2mqtt/config.yaml` **and** `io.hass.version` in `max2mqtt/Dockerfile`. The add-on builds from source on the device, so whatever is on `main` with a new version reaches Home Assistant on the next update. Docs-only changes get no new version.
- **Changelog:** every version has its date as `<sub>YYYY-MM-DD</sub>` under the heading (taken from the git history for older versions). A line `---` and a bold note separate the versions by Dad-Ju (1.2.0–1.3.2) and the original by pwurbs (1.0.0–1.1.0) from this fork (1.3.3 and later). The READMEs list the history by author in the same way.
- **Changes to `main` go through a pull request** in this repository, also docs (`gh pr create --repo dktzde/max2mqtt --base main`, otherwise `gh` targets the upstream).
- **Language:** code, docs, changelog, commits and PR texts in English. Replies to the maintainer in German, in plain language.
- No private data (IP addresses, real RF addresses) in this public repository; examples use placeholders such as `0A1B2C`.
- **Protocol references:** FHEM `10_MAX.pm` (device messages, `desiredTemperature … until`) and `14_CUL_MAX.pm` (CUL side) from [fhem/fhem-mirror](https://github.com/fhem/fhem-mirror) (`fhem/FHEM/`); Cube LAN protocol incl. byte layouts in [Bouni/max-cube-protocol](https://github.com/Bouni/max-cube-protocol). Check there before guessing a message layout.
- Go is installed only for a session (`apk add go`), gone after an add-on restart of the Claude Code add-on.
- **CodeRabbit** (AI review of PRs against our `main`): settings in `.coderabbit.yaml` (#11, 2026-10-31). Release PRs (`chore(release)`) and docs PRs are not reviewed automatically. Active once the maintainer installs the CodeRabbit GitHub app for this repository. Docstring coverage check off (`pre_merge_checks.docstrings.mode: "off"`, 2026-11-01): the upstream code has no docstring rule. Status comments ("review skipped"/"in progress") are off: `reviews.review_status: false` (2026-11-01). Automatic reviews are off too (`reviews.auto_review.enabled: false`, 2026-11-01): CodeRabbit does not auto-review repositories with fewer than 10 stars and still posted a "does not receive automatic reviews" comment on every PR despite `review_status: false`. Reviews start with the PR comment `@coderabbitai review`.

## Known issues and backlog

- Branch `fix-auto-week-program` (for upstream) calls `ApplySetpointAck(dev)` in `auto_mode_test.go`; once `fix-ack-message-counter` is taken upstream, that test needs the counter as on our `main`.
- A NAK (Ack payload bit `0x80`, FHEM `isnak`) is still treated like an Ack.
- The week profile queue is not persisted: after a restart of the add-on, waiting messages are lost and a "waiting" notification stays until it is dismissed by hand.
- A radiator thermostat only reports when something changes, so the valve position arrives irregularly and `0 %` may stay for hours.
- Week profile and config values cannot be read from the devices; they are known only after an import or after the bridge sent them.
- TimeInformation requests (`0x03`) are answered for every device heard, also foreign ones (FHEM answers only `isToMe`). Not fixed on purpose (maintainer's decision 2026-10-28).
- Not covered (see README): eco push buttons as entities, RSSI, Cube polling, fakeWT/fakeSC, Cube "free memory slots".
- Branches based on `upstream/main` conflict with each other in `CHANGELOG.md` (all add `## Unreleased`) and partly in `main.go`; the release merges resolve that.
- Dad-Ju has issues turned off: suggestions only as pull requests.
- `aarch64` is only test-built: CI builds and starts the image on a native arm64 runner (PR #54), the maintainer has no aarch64 hardware.

## Key files

- `max2mqtt/config.yaml`, `max2mqtt/Dockerfile`: app version (both), options, build
- `max2mqtt/CHANGELOG.md`: shown by Home Assistant in the update dialog
- `max2mqtt/main.go`: CUL init (`initializeCUL`), message handling, decoding, climate discovery, presets
- `max2mqtt/transmission_manager.go`: duty cycle credits, normal and bulk queue, verification
- `max2mqtt/ha_notify.go`: Home Assistant notification for week profiles waiting too long
- `max2mqtt/availability.go`, `duty_cycle.go`, `attributes.go`, `persist.go`: bridge status, credit sensors, Cube style attributes, `/data/devices.json`
- `max2mqtt/device_config.go`, `config_input.go`: config entities, known flags, `config/import` and `config/set`
- `max2mqtt/week_profile.go`: week profile (per-day text entities, Cube `programme` format)
- `max2mqtt/shutter_contact.go`, `vacation.go`: shutter contacts, vacation mode
- `max2mqtt/*_test.go`: unit tests (`go test ./...` in `max2mqtt/`)
- `README.md`, `max2mqtt/README.md`, `max2mqtt/DOCS.md`: repository page, app overview, Documentation tab
- `.github/workflows/build.yml`: go vet and tests, image build, start and stop for amd64 and arm64
- `repository.yaml`: app store entry of this fork
