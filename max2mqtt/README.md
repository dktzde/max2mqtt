# MAX! to MQTT Bridge for Home Assistant

## Start here: gateway_id

MAX! devices remember the address of the gateway they were paired with and only accept commands from that address.
The bridge sends with `gateway_id` as its own address, so set it **before the first start**:

| Your situation | `gateway_id` |
|---|---|
| Replacing a **MAX! Cube** | The Cube's RF address – **no re-pairing needed** |
| Coming from **FHEM** (`CUL_MAX`) | The address of your `CUL_MAX` device (`define <name> CUL_MAX <addr>`) |
| New installation | Any 6-digit hex value not used by another MAX! device (e.g. `123456`), then pair all devices |

Format: 6 hex digits without `0x`, e.g. `0A1B2C`.

### Replacing a MAX! Cube without re-pairing

1. **Find the Cube's RF address**, either way:
   - **From the Cube (while it is still running):** Stop the Cube integration or MAX! software first (the Cube accepts only one TCP client), then run `nc <cube-ip> 62910`.
     The first line is `H:<serial>,<rf-address>,<firmware>,...`, e.g. `H:KEQ0123456,0a1b2c,0113,...` → `0A1B2C`.
     The serial number on the Cube's label (`KEQ…`) is **not** the RF address.
   - **From the radio (Cube already switched off):** Start the bridge with `log_level: debug` and any `gateway_id`.
     Turn the wheel of a thermostat or open and close a window with a shutter contact.
     In the log, `Parsed Packet detail` of the state messages (`type=0x60` radiator thermostat, `0x70` wall thermostat, `0x30` shutter contact) shows `dst=<address>`.
     That is the address your devices still report to – the Cube's RF address (ignore `dst=000000` and addresses of your own devices).
2. **Switch the Cube off for good.** Two gateways with the same address would both acknowledge and send, and the Cube would overwrite settings.
3. Set `gateway_id` to the Cube's RF address and restart the add-on.
4. **Check:** devices stop repeating their messages (the CUL now acknowledges them, see [Auto-Ack](DOCS.md#auto-ack-za)), the RF error sensors turn off, and a setpoint change from Home Assistant is confirmed in the log (`Short Ack applied pending setpoint`, no retry).

Nothing has to be transferred for the devices to work: week programs, eco/comfort temperatures and associations (e.g. shutter contact ↔ thermostat) are stored in the devices.
The bridge cannot read them back over the radio, though. To see them in Home Assistant (attributes, config entities, presets eco/comfort/Window Open) and to change single values safely, import them once from the Cube integration: see [Migrating from the MAX! Cube integration](DOCS.md#migrating-from-the-max-cube-integration).
The room/group IDs are learned automatically from the first received messages.

## About

If you have an existing MAX! installation with e.g. FHEM and you want to migrate to Home Assistant, this might be a solution for you.

I personally want to keep at least my MAX! wall thermostats until there are good alternatives available (battery driven, affordable, modern looking, display...).
But the current solutions to integrate MAX! components into Home Assistant are not ideal:
- The official integration requires the ugly MAX! cube (I threw away this device years ago due to instability)
- The available MAX! add-ons are not really maintained
- The usually recommended usage of Homegear is not a good option: Not maintained anymore and much to overloaded.

Additionally, I was curious to see if I could use AI to help me with the development of this bridge.

This add-on is a lightweight Go-based bridge connecting a CUL (USB serial or network TCP, 868MHz MAX! protocol) to Home Assistant via MQTT. It focuses on usage of an existing MAX! installation with wall thermostats and radiator valves, only supporting the really required functions to be used with Home Assistant. It's not intended as a full replacement of MAX! Cube or the implementation contained in FHEM.

## History

Who made what, newest first. Full list with dates: [CHANGELOG](CHANGELOG.md).

- **This fork, [dktzde/max2mqtt](https://github.com/dktzde/max2mqtt)** (since 2026-10-24): 1.3.3 and later
- **[Dad-Ju/max2mqtt](https://github.com/Dad-Ju/max2mqtt)** (2026-10-15 to 2026-10-16): 1.2.0 to 1.3.2
- **Original by [pwurbs/max2mqtt](https://github.com/pwurbs/max2mqtt)** (December 2025 to January 2026): 1.0.0 and 1.1.0

### v1.4.1 (2026-10-28, this fork) — Week profile wait time

- Week profile messages wait up to 60 minutes (before: 30) for duty cycle credits before they are discarded

### v1.4.0 (2026-10-28, this fork) — Drop-in replacement for the MAX! Cube integration

This release makes the bridge a drop-in replacement for the MAX! Cube integration in Home Assistant (apart from the service `maxcube.set_programme`, which becomes the MQTT topic `max/<id>/programme/set` with the same data).

- **Device data survives restarts** in `/data/devices.json`: state (mode, setpoint), valve position, room/group ID, device type, config values (and whether they are known), week profile. Before, a setpoint change after a restart switched an auto thermostat to manual
- **Climate attributes like the MAX! Cube integration** on `max/<id>/attributes`: `valve_position`, `programme`, `comfort_temp`, `eco_temp`, `window_open_temp`, `window_open_duration` (minutes), `temp_offset`, `boost_value`, `boost_duration`, `decalc_day`, `decalc_time`, `max_valve`, `valve_offset`, `room`, `device_rf_address`
- **`hvac_action`** of radiator thermostats follows the valve (heating while open), as in the Cube integration
- **Presets like the MAX! Cube integration:** `comfort`, `eco`, `Window Open`, `On` (30.5 °C), `boost`, derived from the setpoint; temperature presets keep the mode; `none` ends a boost (back to the mode and setpoint before it) or switches to auto (week program). Max temperature 30.5 °C

### v1.3.7 (2026-10-28, this fork) — Config values and week profile

- **Unknown config values:** the bridge cannot read a device's settings. Until it knows them, the config entities show *unknown*, and changing a single value is refused instead of overwriting the device's other settings with defaults (temperature group `0x11`, valve group `0x12`). New topics `max/<id>/config/import` (store the real values, e.g. from a MAX! Cube, without sending) and `max/<id>/config/set` (store and send). Presets eco/comfort, and vacation without a temperature, need known values
- **Week profile:** the second message of a day is only sent when the day has more than 7 control points (as FHEM does); unchanged days are skipped; one text entity per day in FHEM syntax (`17,06:00,21,22:00,17`) replaces the JSON text entity that could not hold a week (255 characters); new topics `max/<id>/programme/set` (MAX! Cube `set_programme` format) and `max/<id>/programme/import` (store only); the profile is published on `max/<id>/programme` and `max/<id>/week_profile/<day>`
- Remove a build output committed by mistake in 1.3.6

### v1.3.6 (2026-10-28, this fork) — Duty cycle and availability

- **Wait for duty cycle credits:** a command that finds too few credits (or a busy CUL queue) is no longer dropped at once; the bridge queries again every 15 s and sends it when there are enough, until `command_timeout`. Week profile messages wait up to 60 minutes as a bulk transfer and let setpoint, mode and config commands go first
- **Availability:** `max/bridge/status` (`online`/`offline`, MQTT last will, lost CUL connection, shutdown); all entities use it and become unavailable instead of showing old values
- **Duty cycle sensors:** "Duty Cycle" (%) and "TX Credits" on the bridge device, from the CUL credit report polled every minute

### v1.3.5 (2026-10-28, this fork) — Boost, preset and auto mode fixes

- **Preset after boost:** the climate preset follows the device status: a boost that ends on the device (or an eco/comfort setpoint changed at the wheel) resets the preset to `none` instead of keeping `boost` until the next command from Home Assistant
- **Boost setpoint:** boost is sent with temperature 0 (as FHEM does) and the cached setpoint is kept, so `heat`, preset `none` or `auto` after a boost from Home Assistant no longer set 30 °C
- **Actual temperature 0:** status messages without a new measurement (actual temperature 0, or the rare values below 1 °C) keep the last measured value instead of showing 0 °C and `heating`
- **Auto mode:** HVAC mode `auto` follows the week program (sent with temperature 0, like FHEM and the MAX! Cube) instead of holding the current setpoint until the next switch point

### v1.3.4 (2026-10-26, this fork) — Vacation mode

- **Vacation mode:** `max/<id>/vacation/set` with `{"temperature": 12, "until": "2026-11-02 18:00"}` holds the temperature until that date/time, then the thermostat returns to auto by itself; `off` ends it early. End date as "Vacation Until" sensor. Details in DOCS

### v1.3.3 (2026-10-25, this fork) — Auto-Ack, valve position, shutter contacts

- **Auto-Ack (`Za`):** the CUL acknowledges every message sent to `gateway_id` by itself (like FHEM), so devices no longer repeat their messages or set their RF error bit, saving battery and duty cycle
- **Valve position** sensor (0–100 %) for each radiator thermostat, retained on `max/<id>/valve`
- **RF error** diagnostic binary sensor (`rf_error` in `max/<id>/state`)
- **Shutter contacts** (`0x30`): window open/closed, battery and RF error binary sensors; link them with a radiator thermostat, see [Device Association](#device-association-linking-devices)

### v1.3.0 (2026-10-16, Dad-Ju) — Device configuration in Home Assistant

Exposes FHEM-compatible settings as MQTT discovery entities (`entity_category: config`), with **Ack/push confirmation** (no polling):

- **Climate presets:** eco, comfort, boost
- **Temperatures (`0x11`):** eco/comfort/min/max, measurement offset, window-open temp/duration
- **Valve (`0x12`, radiators):** boost duration/position, decalc day/hour, max valve, valve offset
- **Other:** display mode, group ID, disassociate partner, factory reset, week profile
- Retained state on `max/<id>/config`; UI updates when the device Acks over CUL

### v1.2.0 (2026-10-15, Dad-Ju) — Network CUL

| Transport | `serial_port` value | `baud_rate` |
|-----------|---------------------|-------------|
| USB serial | Device path, e.g. `/dev/serial/by-id/...` | Required (default `38400`) |
| Network TCP | `tcp://host:port` or `telnet://host:port` | Ignored |

```yaml
serial_port: "tcp://192.168.1.50:2323"
baud_rate: 38400
mqtt_broker: "tcp://homeassistant.local:1883"
gateway_id: "123456"   # see "Start here: gateway_id" above
```

See [DOCS.md](DOCS.md) for Network CUL and Device Configuration details.

## Features

This bridge is intended and most suited to migrate an existing FHEM-operated MAX! installation to Home Assistant. In this case, the thermostats keep their configuration (Eco/Comfort temperature, associations, week plans, etc.) and the bridge only provides the current state to Home Assistant.
But it should work with fresh devices too...
These are the features:

-   **CUL Communication**: Reads and sends `Z` telegrams from CUL-FW compatible devices over USB serial or TCP (`tcp://host:port`).
-   **Auto-Ack**: The CUL acknowledges all messages sent to `gateway_id` (`Za`), so devices do not repeat them.
-   **MAX! Protocol Parsing**: Decodes Temperature, Mode, Battery State, Valve Position, RF error and Window State.
-   **Time Synchronization**: Automatically syncs time with devices.
-   **Pairing Mode**: Trigger via MQTT (`max/bridge/pair`) to pair new devices.
-   **Association Management**: Trigger via MQTT (`max/bridge/associate`) to associate radiator valves with wall thermostats.
-   **Battery**: Reports battery state (OK/Low) for thermostats and shutter contacts.
-   **Thermostat Configuration**: Full ConfigTemperatures / ConfigValve / display / group / week profile via HA config entities; climate presets comfort/eco/Window Open/On/boost; Ack-confirmed state (CUL push).
-   **TX Management**:
    - **Duty Cycle Management**: Adheres to 868MHz 1% transmission limits using CUL's credit system. When the configurable minimum threshold is exceeded, the bridge will not send any commands and the old state is restored.
    - **Buffer Management**: Detects LOVF (Limit Of Voice Full) or CUL buffer overflow errors (configurable thresholds).
    - **TX Verification**: To improve reliability for "Set Temperature" commands, a verification mechanism is implemented. If there is no RX packet containing the expected temperature within a configurable timeout, the TX packet is sent again once.

### Home Assistant Integration
-   **Auto-Discovery**: Fully compliant with HA MQTT Discovery (Climate, Number, Select, Binary Sensor).
-   **Entities**:
    - **Climate**: Thermostat control with support for `temperature` (setpoint), `current_temperature` (actual), `hvac_action` (heating/idle; radiator thermostats: heating while the valve is open), and `hvac_mode` (auto/heat/off).
    - **Climate attributes** like the MAX! Cube integration (`max/<id>/attributes`): `valve_position`, `programme`, `comfort_temp`, `eco_temp`, `window_open_temp`, `window_open_duration` (minutes), `temp_offset`, `boost_value`, `boost_duration`, `decalc_day` (0 = Saturday), `decalc_time`, `max_valve`, `valve_offset`, `room`, `device_rf_address`. Config values and the programme appear once they are known.
    - **Number / Select / Text / Button**: Config entities (eco/comfort/min/max/offset/window/valve/display/group/week profile/disassociate/factory reset).
    - **Climate presets** like the MAX! Cube integration: comfort, eco, Window Open, On, boost; `none` ends a boost (back to the mode before) or switches to auto.
    - **Vacation mode**: hold a temperature until a date/time via `max/<id>/vacation/set` (see DOCS); end date as "Vacation Until" sensor.
    - **Binary Sensor**: Battery status (OK/Low), RF error (diagnostic: device cannot reach its link partner/gateway).
    - **Sensor**: Valve position in % (radiator thermostats, retained on `max/<id>/valve`).
    - **Shutter Contact**: Window binary sensor (open/closed) plus battery and RF error (diagnostic), state on `max/<id>/state`.
-   **Pairing Mode**: Trigger via MQTT (`max/bridge/pair`) to pair new devices.
-   **Association**: Trigger via MQTT (`max/bridge/associate`) to associate radiator valves with wall thermostats.
-   **Availability**: `max/bridge/status` is `online` while the bridge is connected to MQTT and to the CUL, otherwise `offline` (MQTT last will, lost CUL connection, shutdown). All entities use it, so Home Assistant shows them as unavailable instead of keeping old values.
-   **Duty cycle**: "Duty Cycle" (used part of the CUL credit budget in %, like the MAX! Cube's value) and "TX Credits" sensors on the bridge device, polled every minute (`X`, no radio traffic).
-   **Retained Messages**: All MQTT messages (discovery and state) are published with the `retain` flag. This ensures Home Assistant keeps entities and their last known values across bridge or HA restarts.
-   **Default State**: Newly discovered devices without explicit mode information default to `heat` mode to avoid "Invalid mode" errors in Home Assistant.
-   **MAX! mode mapping**:
    - auto: auto
    - manual (>4.5°C): heat
    - manual (<=4.5°C): off
    - Vacation: off (end date in the "Vacation Until" sensor)
    - Boost: heat

### Not covered
- Signal strength (RSSI) as sensor
- Eco push-buttons as first-class entities
- Cube/MAXLAN polling
- fakeWT / fakeSC (external sensor simulation)

Prefer Home Assistant schedules for day-to-day heating; the device week profile can be set per day (text entities) or in MAX! Cube format, see DOCS.

## Installation and Configuration as Home Assistant Add-on

1.  Prepare the Add-on:
    - Either copy this directory to your Home Assistant `addons/local/max2mqtt` folder.
    - Or use the Add-on Store in Home Assistant: Add the repository `https://github.com/dktzde/max2mqtt` (this fork: Auto-Ack, valve position and shutter contacts, on top of Dad-Ju's network CUL support and device configuration). Upstream: `https://github.com/Dad-Ju/max2mqtt`, originally `https://github.com/pwurbs/max2mqtt`.
2.  Navigate to the Add-on Store, either the Local addon-ons section or the configured repository.
3.  Install **MAX! to MQTT Bridge**.
4.  Configure the parameters like described below – set `gateway_id` first, see [Start here](#start-here-gateway_id).
5.  Start the Add-on.

### Quick start: Network CUL

If your CUL is reachable over TCP (CUN/CUNO, ESP/ser2net, etc.):

1. Set `serial_port` to `tcp://<ip>:<port>` (e.g. `tcp://192.168.1.50:2323`).
2. Leave `baud_rate` at the default; it is not used for TCP.
3. You do **not** need to map USB devices in the add-on for a network CUL.
4. Start the add-on and check the log for `Opened CUL TCP connection`.

USB sticks still use a path such as `/dev/serial/by-id/...` plus `baud_rate` as before.

## Configuration Options

| Environment Variable | JSON Option | Default | Description |
|---------------------|-------------|---------|-------------|
| `SERIAL_PORT` | `serial_port` | `/dev/serial/by-id/usb-SHK_NANO_CUL_868-if00-port0` | Path to CUL stick, or network URL `tcp://host:port` (also `telnet://host:port`) |
| `BAUD_RATE` | `baud_rate` | `38400` | Serial baud rate (ignored for TCP/network CUL) |
| `MQTT_BROKER` | `mqtt_broker` | `tcp://homeassistant:1883` | MQTT Broker URL |
| `MQTT_USER` | `mqtt_user` | (empty) | MQTT Username |
| `MQTT_PASS` | `mqtt_pass` | (empty) | MQTT Password |
| `GATEWAY_ID` | `gateway_id` | `123456` | Gateway address, 6 hex digits. Use your old Cube / FHEM `CUL_MAX` address to keep existing pairings, see [Start here](#start-here-gateway_id) |
| `LOG_LEVEL` | `log_level` | `info` | Log verbosity (debug/info/warning/error) |
| `DUTY_CYCLE_MIN_CREDITS` | `duty_cycle_min_credits` | `200` | Min credits to allow sending |
| `COMMAND_TIMEOUT` | `command_timeout` | `1m` | Max time a command waits for duty cycle credits before it is discarded (week profile messages: 60 min) |
| `MAX_CUL_QUEUE` | `max_cul_queue` | `5` | Max number of commands allowed in CUL stick buffer before throttling |
| `TX_CHECK_PERIOD` | `tx_check_period` | `1m` | Duration to wait for device verification before retrying Set Temp commands |

> **Note on Configuration:**
> *   **Home Assistant Add-on:** The application automatically reads configuration from `/data/options.json`. This file is generated by the Home Assistant Supervisor at runtime based on the values defined in `config.yaml`.
> *   **Standalone / Docker:** Configuration via Environment Variables takes precedence if `/data/options.json` is not found.

## Pairing New Devices

To pair a new MAX! device via Home Assistant UI:
1.  Put the device in pairing mode (usually by holding the boost button).
2.  Trigger the bridge's pairing mode by sending a message to `max/bridge/pair`.
    1.  Go to **Settings** -> **Devices & Services**.
    2.  Find the **MQTT** integration and click **Configure**.
    3.  Under "Publish a packet":
        - Topic: `max/bridge/pair`
        - Payload: `on`
        - Click **Publish**.
3.  The bridge will respond to pairing requests for 60 seconds.
4.  Watch the log output (at least info level required) to see the pairing process.

## Device Association (Linking Devices)

You can link MAX! devices together so they work as a team. For example, linking a **radiator thermostat** with a **wall thermostat** allows the wall thermostat to control the radiator valve based on the room temperature it measures.

> **Important**: Association must be done **in both directions**. Each device needs to know about the other.

**Partner Type Values:**
| Type | Device |
|------|--------|
| 1 | Heating Thermostat (radiator valve) |
| 2 | Heating Thermostat Plus |
| 3 | Wall Mounted Thermostat |
| 4 | Shutter Contact |
| 5 | Push Button |

**Example**: Link radiator `0A1B2C` with wall thermostat `0D3E4F`:

| Field | Value |
|-------|-------|
| Topic | `max/0A1B2C/associate` |
| Payload | `0D3E4F:3` |

**Example**: Link shutter contact `1A2B3C` with radiator `0A1B2C` (the thermostat then switches to its *Window Open Temperature* while the window is open):

| Topic | Payload |
|-------|---------|
| `max/0A1B2C/associate` | `1A2B3C:4` |
| `max/1A2B3C/associate` | `0A1B2C:1` |

Use the Home Assistat UI to publish the MQTT messages.

## More Information

Find more detailled information on the Documentation tab of the "MAX! to MQTT Bridge" addon configuration in the Home Assistant settings.

## Feedback

Please open an issue on [GitHub](https://github.com/dktzde/max2mqtt) if you find any bugs or have suggestions for improvements.