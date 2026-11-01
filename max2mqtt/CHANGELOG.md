## 1.5.1

<sub>2026-11-01</sub>

- **Acks are matched by message counter:** a config change (e.g. measurement offset) or a setpoint is confirmed only by the device's Ack for that telegram, addressed to the gateway (as FHEM does). Before, any Ack from the device confirmed the pending change, e.g. the Ack for the previous week profile day, the Ack for the WakeUp before a SetTemperature, or an Ack to a linked device, so a change that never reached the device was shown as confirmed. Now such a change goes back to the previous value after the 30 s Ack timeout

## 1.5.0

<sub>2026-11-01</sub>

- **Duty cycle with a-culfw:** the bridge reads the CUL's credit maximum from its version line (culfw 900, a-culfw 3600, e.g. a MAX! Cube flashed with a-culfw) and raises it when a credit report shows more. Before, the maximum was fixed at 900, so the Duty Cycle sensor showed 0 % on a-culfw most of the time
- The credit maximum starts again at 900 on every new CUL connection and is then set from that CUL's version line, also for standard culfw (`V 1.67 CUL868`), so a maximum of an earlier stick cannot stay
- **Week profile reserve** follows the same maximum: with a-culfw and the defaults, week profile messages wait for 920 credits (200 + 20 % of 3600) instead of 380

## 1.4.3

<sub>2026-10-28</sub>

- **Week profile messages are never discarded** for lack of duty cycle credits (before: after 120 minutes); the "discarded" notification is gone, the notification after 60 minutes stays
- **Credit reserve for direct commands:** week profile messages are only sent while the credits are above `duty_cycle_min_credits` plus `week_profile_reserve_percent` (new option, default 20 %) of the CUL's maximum of 900 credits, i.e. 380 with the defaults, so setpoint, mode and config changes can still be sent at once

## 1.4.2

<sub>2026-10-28</sub>

- Week profile messages wait up to 120 minutes (before: 60) for duty cycle credits
- **Home Assistant notification** when a week profile message still waits after 60 minutes (it keeps waiting); removed when the profile was sent, replaced by a "discarded" notification after 120 minutes. The add-on now asks for `homeassistant_api` (Home Assistant shows the new permission on update)

## 1.4.1

<sub>2026-10-28</sub>

- Week profile messages wait up to 60 minutes (before: 30) for duty cycle credits before they are discarded

## 1.4.0

<sub>2026-10-28</sub>

This release makes the bridge a drop-in replacement for the MAX! Cube integration in Home Assistant (apart from the service `maxcube.set_programme`, which becomes the MQTT topic `max/<id>/programme/set` with the same data).

- **Device data survives restarts** in `/data/devices.json`: state (mode, setpoint), valve position, room/group ID, device type, config values (and whether they are known), week profile. Before, a setpoint change after a restart switched an auto thermostat to manual
- **Climate attributes like the MAX! Cube integration** on `max/<id>/attributes`: `valve_position`, `programme`, `comfort_temp`, `eco_temp`, `window_open_temp`, `window_open_duration` (minutes), `temp_offset`, `boost_value`, `boost_duration`, `decalc_day`, `decalc_time`, `max_valve`, `valve_offset`, `room`, `device_rf_address`
- **`hvac_action`** of radiator thermostats follows the valve (heating while open), as in the Cube integration
- **Presets like the MAX! Cube integration:** `comfort`, `eco`, `Window Open`, `On` (30.5 °C), `boost`, derived from the setpoint; temperature presets keep the mode; `none` ends a boost (back to the mode and setpoint before it) or switches to auto (week program). Max temperature 30.5 °C

## 1.3.7

<sub>2026-10-28</sub>

- **Unknown config values:** the bridge cannot read a device's settings. Until it knows them, the config entities show *unknown*, and changing a single value is refused instead of overwriting the device's other settings with defaults (temperature group `0x11`, valve group `0x12`). New topics `max/<id>/config/import` (store the real values, e.g. from a MAX! Cube, without sending) and `max/<id>/config/set` (store and send). Presets eco/comfort, and vacation without a temperature, need known values
- **Week profile:** the second message of a day is only sent when the day has more than 7 control points (as FHEM does); unchanged days are skipped; one text entity per day in FHEM syntax (`17,06:00,21,22:00,17`) replaces the JSON text entity that could not hold a week (255 characters); new topics `max/<id>/programme/set` (MAX! Cube `set_programme` format) and `max/<id>/programme/import` (store only); the profile is published on `max/<id>/programme` and `max/<id>/week_profile/<day>`
- Remove a build output committed by mistake in 1.3.6

## 1.3.6

<sub>2026-10-28</sub>

- **Wait for duty cycle credits:** a command that finds too few credits (or a busy CUL queue) is no longer dropped at once; the bridge queries again every 15 s and sends it when there are enough, until `command_timeout`. Week profile messages wait up to 30 minutes as a bulk transfer and let setpoint, mode and config commands go first
- **Availability:** `max/bridge/status` (`online`/`offline`, MQTT last will, lost CUL connection, shutdown); all entities use it and become unavailable instead of showing old values
- **Duty cycle sensors:** "Duty Cycle" (%) and "TX Credits" on the bridge device, from the CUL credit report polled every minute

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