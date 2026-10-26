## Unreleased

- **Vacation mode** for radiator and wall thermostats: `max/<id>/vacation/set` with `{"temperature": 12, "until": "2026-11-02 18:00"}` holds the temperature until that date/time, then the device returns to auto by itself (`temperature` optional, default: the device's eco temperature; `off` ends it early). SetTemperature `0x40` with the three "until" bytes, as in FHEM `10_MAX.pm`
- Decode the vacation end date from status messages (`0x60`/`0x70`/Acks/`0x40`): `until` in `max/<id>/state` and a "Vacation Until" sensor. In vacation mode bytes 3-5 are no longer read as the actual temperature

## 1.3.2

- Apply expected setpoint on short device Ack (FHEM AckSetTemperature) — verification no longer requires a full status payload
- Send WakeUp (`0xF1`) before SetTemperature/mode when the device has been quiet
- Richer `log_level: debug` output for CUL RX/TX, SetTemp encoding, packet headers, and Ack tracking (credit polls stay debug-only)

## 1.3.1

- Fix SetTemperature/mode TX: use learned room `groupId` with flag `0x04` (FHEM/Cube behaviour)
- Do not drop TX when credit query times out; send with warning instead of silently restoring HA state
- Normalize device addresses to uppercase for MQTT commands

## 1.3.0

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

- **Network CUL (TCP):** `serial_port` accepts `tcp://host:port` or `telnet://host:port` (raw TCP; baud rate ignored)
- USB serial path unchanged; reconnect loop works for both transports
- Docs: Network CUL section in DOCS.md / README; Dockerfile strips Windows CRLF on `run.sh`

## 1.1.0

- Managing the duty cycle
- Improved RX packet management
- Now managing radiator thermostats too
- Added TX validation

## 1.0.0

- Initial release