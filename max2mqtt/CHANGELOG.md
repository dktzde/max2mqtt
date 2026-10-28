## 1.3.5

<sub>2026-10-28</sub>

- **Preset after boost:** the climate preset follows the device status: a boost that ends on the device (or an eco/comfort setpoint changed at the wheel) resets the preset to `none` instead of keeping `boost` until the next command from Home Assistant
- **Boost setpoint:** boost is sent with temperature 0 (as FHEM does) and the cached setpoint is kept, so `heat`, preset `none` or `auto` after a boost from Home Assistant no longer set 30 °C
- **Actual temperature 0:** status messages without a new measurement (actual temperature 0, or the rare values below 1 °C) keep the last measured value instead of showing 0 °C and `heating`
- **Auto mode:** HVAC mode `auto` follows the week program (sent with temperature 0, like FHEM and the MAX! Cube) instead of holding the current setpoint until the next switch point

## 1.3.4

<sub>2026-10-26</sub>

- **Vacation mode** for radiator and wall thermostats: `max/<id>/vacation/set` with `{"temperature": 12, "until": "2026-11-02 18:00"}` holds the temperature until that date/time, then the device returns to auto by itself (`temperature` optional, default: the device's eco temperature; `off` ends it early). SetTemperature `0x40` with the three "until" bytes, as in FHEM `10_MAX.pm`
- Decode the vacation end date from status messages (`0x60`/`0x70`/Acks/`0x40`): `until` in `max/<id>/state` and a "Vacation Until" sensor. In vacation mode bytes 3-5 are no longer read as the actual temperature

## 1.3.3

<sub>2026-10-25</sub>

- Set culfw auto-ack address (`Za<gateway_id>`) on CUL init, so devices get an Ack for messages sent to the gateway; ignore the CUL's own auto-Acks
- Valve position sensor (0-100 %) for radiator thermostats from ThermostatState (`0x60`) and their Acks, retained on `max/<id>/valve`
- RF error diagnostic binary sensor from status bit 6 (`rf_error` in `max/<id>/state`)
- Shutter contacts (`0x30`): window, battery and RF error binary sensors via MQTT discovery

---

**Versions 1.2.0 to 1.3.2 (October 2026) are by [Dad-Ju/max2mqtt](https://github.com/Dad-Ju/max2mqtt).** This fork ([dktzde/max2mqtt](https://github.com/dktzde/max2mqtt), since 2026-10-24) continues from 1.3.3. Dates are taken from the git history.

## 1.3.2

<sub>2026-10-16</sub>

- Apply expected setpoint on short device Ack (FHEM AckSetTemperature) — verification no longer requires a full status payload
- Send WakeUp (`0xF1`) before SetTemperature/mode when the device has been quiet
- Richer `log_level: debug` output for CUL RX/TX, SetTemp encoding, packet headers, and Ack tracking (credit polls stay debug-only)

## 1.3.1

<sub>2026-10-16</sub>

- Fix SetTemperature/mode TX: use learned room `groupId` with flag `0x04` (FHEM/Cube behaviour)
- Do not drop TX when credit query times out; send with warning instead of silently restoring HA state
- Normalize device addresses to uppercase for MQTT commands

## 1.3.0

<sub>2026-10-16</sub>

### Features
- Full device configuration via HA MQTT discovery (`entity_category: config`)
- **ConfigTemperatures (`0x11`)**: eco/comfort/min/max, measurement offset, window-open temp & duration; retained state on `max/<id>/config`
- **ConfigValve (`0x12`)**: boost duration/position, decalc day/hour, max valve, valve offset (radiator thermostats)
- **Climate presets**: eco, comfort, boost (HA `preset_modes`)
- **Linking / safety**: disassociate partner, group ID, factory-reset button, bridge pair button
- **Week profile (`0x10`)**: JSON via text entity (advanced; prefer HA schedules for daily use)

### Behaviour
- Ack/push confirmation over CUL (no polling); optimistic MQTT update, authoritative republish on device Ack
- Ack timeout (~30s) restores previous retained config values
- Short Acks confirm config TX; longer Acks/status still update climate state

### Docs
- README/DOCS: configuration entities, push/Ack flow, week-profile JSON format

## 1.2.0

<sub>2026-10-15</sub>

- **Network CUL (TCP):** `serial_port` accepts `tcp://host:port` or `telnet://host:port` (raw TCP; baud rate ignored)
- USB serial path unchanged; reconnect loop works for both transports
- Docs: Network CUL section in DOCS.md / README; Dockerfile strips Windows CRLF on `run.sh`

---

**Versions 1.0.0 and 1.1.0 (December 2025 to January 2026) are the original add-on by [pwurbs/max2mqtt](https://github.com/pwurbs/max2mqtt).** Dad-Ju's fork continues from 1.2.0.

## 1.1.0

<sub>2025-12-20 to 2026-01-15 (changes kept this version number)</sub>

- Managing the duty cycle
- Improved RX packet management
- Now managing radiator thermostats too
- Added TX validation

## 1.0.0

<sub>before 2025-12-20 (not in the public git history)</sub>

- Initial release