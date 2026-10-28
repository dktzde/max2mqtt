# MAX! to MQTT Bridge for Home Assistant

If you have an existing MAX! installation with e.g. FHEM and you want to migrate to Home Assistant, this might be a solution for you.

I personally want to keep at least my MAX! wall thermostats until there are good alternatives available (battery driven, affordable, modern looking, display...).
But the current solutions to integrate MAX! components into Home Assistant are not ideal:
- The official integration requires the ugly MAX! cube (I threw away this device years ago due to instability)
- The available MAX! add-ons are not really maintained
- The usually recommended usage of Homegear is not a good option: Not maintained anymore and much to overloaded.

Additionally, I was curious to see if I could use AI to help me with the development of this bridge.

This add-on is a lightweight Go-based bridge connecting a CUL (USB serial or network TCP, 868MHz MAX! protocol) to Home Assistant via MQTT. It focuses on usage of an existing MAX! installation with wall thermostats and radiator valves, only supporting the really required functions to be used with Home Assistant. It's not intended as a full replacement of MAX! Cube or the implementation contained in FHEM.

## What's new in this fork

### v1.3.0 — Device configuration in Home Assistant

Exposes FHEM-compatible settings as MQTT discovery entities (`entity_category: config`), with **Ack/push confirmation** (no polling):

- **Climate presets:** eco, comfort, boost
- **Temperatures (`0x11`):** eco/comfort/min/max, measurement offset, window-open temp/duration
- **Valve (`0x12`, radiators):** boost duration/position, decalc day/hour, max valve, valve offset
- **Other:** display mode, group ID, disassociate partner, factory reset, week profile JSON
- Retained state on `max/<id>/config`; UI updates when the device Acks over CUL

### v1.2.0 — Network CUL

| Transport | `serial_port` value | `baud_rate` |
|-----------|---------------------|-------------|
| USB serial | Device path, e.g. `/dev/serial/by-id/...` | Required (default `38400`) |
| Network TCP | `tcp://host:port` or `telnet://host:port` | Ignored |

```yaml
serial_port: "tcp://192.168.1.50:2323"
baud_rate: 38400
mqtt_broker: "tcp://homeassistant.local:1883"
gateway_id: "123456"
```

See [DOCS.md](DOCS.md) for Network CUL and Device Configuration details.

## Features

This bridge is intended and most suited to migrate an existing FHEM-operated MAX! installation to Home Assistant. In this case, the thermostats keep their configuration (Eco/Comfort temperature, associations, week plans, etc.) and the bridge only provides the current state to Home Assistant.
But it should work with fresh devices too...
These are the features:

-   **CUL Communication**: Reads and sends `Z` telegrams from CUL-FW compatible devices over USB serial or TCP (`tcp://host:port`).
-   **MAX! Protocol Parsing**: Decodes Temperature, Mode, and Battery State.
-   **Time Synchronization**: Automatically syncs time with devices.
-   **Pairing Mode**: Trigger via MQTT (`max/bridge/pair`) to pair new devices.
-   **Association Management**: Trigger via MQTT (`max/bridge/associate`) to associate radiator valves with wall thermostats.
-   **Battery**: Reports battery state (OK/Low) for thermostats.
-   **Thermostat Configuration**: Full ConfigTemperatures / ConfigValve / display / group / week profile via HA config entities; climate presets eco/comfort/boost; Ack-confirmed state (CUL push).
-   **TX Management**:
    - **Duty Cycle Management**: Adheres to 868MHz 1% transmission limits using CUL's credit system. When the configurable minimum threshold is exceeded, the bridge will not send any commands and the old state is restored.
    - **Buffer Management**: Detects LOVF (Limit Of Voice Full) or CUL buffer overflow errors (configurable thresholds).
    - **TX Verification**: To improve reliability for "Set Temperature" commands, a verification mechanism is implemented. If there is no RX packet containing the expected temperature within a configurable timeout, the TX packet is sent again once.

### Home Assistant Integration
-   **Auto-Discovery**: Fully compliant with HA MQTT Discovery (Climate, Number, Select, Binary Sensor).
-   **Entities**:
    - **Climate**: Thermostat control with support for `temperature` (setpoint), `current_temperature` (actual), `hvac_action` (heating/idle), and `hvac_mode` (auto/heat/off).
    - **Number / Select / Text / Button**: Config entities (eco/comfort/min/max/offset/window/valve/display/group/week profile/disassociate/factory reset).
    - **Climate presets**: eco, comfort, boost.
    - **Binary Sensor**: Battery status (OK/Low).
-   **Pairing Mode**: Trigger via MQTT (`max/bridge/pair`) to pair new devices.
-   **Association**: Trigger via MQTT (`max/bridge/associate`) to associate radiator valves with wall thermostats.
-   **Retained Messages**: All MQTT messages (discovery and state) are published with the `retain` flag. This ensures Home Assistant keeps entities and their last known values across bridge or HA restarts.
-   **Default State**: Newly discovered devices without explicit mode information default to `heat` mode to avoid "Invalid mode" errors in Home Assistant.
-   **MAX! mode mapping**:
    - auto: auto
    - manual (>4.5°C): heat
    - manual (<=4.5°C): off
    - Vacation: off
    - Boost: heat

### Not covered
- Processing live valve position / signal strength as sensors
- Shutter contacts and eco push-buttons as first-class entities
- Cube/MAXLAN polling
- fakeWT / fakeSC (external sensor simulation)

Prefer Home Assistant schedules for day-to-day heating; device week profile JSON is available for advanced use.

## Installation and Configuration as Home Assistant Add-on

1.  Prepare the Add-on:
    - Either copy this directory to your Home Assistant `addons/local/max2mqtt` folder.
    - Or use the Add-on Store in Home Assistant: Add the repository `https://github.com/Dad-Ju/max2mqtt` (this fork, includes TCP CUL support). Upstream: `https://github.com/pwurbs/max2mqtt`.
2.  Navigate to the Add-on Store, either the Local addon-ons section or the configured repository.
3.  Install **MAX! to MQTT Bridge**.
4.  Configure the parameters like described below.
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
| `GATEWAY_ID` | `gateway_id` | `123456` | Gateway Hex ID (3 bytes) |
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

Use the Home Assistat UI to publish the MQTT messages.

## More Information

Find more detailled information on the Documentation tab of the "MAX! to MQTT Bridge" addon configuration in the Home Assistant settings.

## Feedback

Please open an issue on [GitHub](https://github.com/Dad-Ju/max2mqtt) if you find any bugs or have suggestions for improvements.