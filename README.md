# MAX! to MQTT Bridge Add-on Repository

<p align="center">
   <img src="logo.png" width="300" alt="Home Assistant Add-on MAX! to MQTT Bridge">
</p>

## Start here: gateway_id

MAX! devices only accept commands from the gateway address they were paired with. Set `gateway_id` **before the first start**:

- **Replacing a MAX! Cube:** use the Cube's RF address – no re-pairing needed. Read it from the Cube (`nc <cube-ip> 62910`, first line `H:<serial>,<rf-address>,…`) or from the radio (`log_level: debug`, `dst=` of thermostat/window messages). Then switch the Cube off for good.
- **Coming from FHEM:** use the address of your `CUL_MAX` device.
- **New installation:** any unused 6-digit hex value (e.g. `123456`), then pair all devices.

Step-by-step guide: [Start here: gateway_id](max2mqtt/README.md#start-here-gateway_id).

This repository contains the MAX! to MQTT Bridge Home Assistant Add-on. 

If you have an existing MAX! installation with e.g. FHEM and you want to migrate to Home Assistant, this might be a solution for you.

I personally want to keep at least my MAX! wall thermostats until there are good alternatives available (battery driven, affordable, modern looking, display...).
But the current solutions to integrate MAX! components into Home Assistant are not ideal:
- The official integration requires the ugly MAX! cube (I threw away this device years ago due to instability)
- The available MAX! add-ons are not really maintained
- The usually recommended usage of Homegear is not a good option: Not maintained anymore and much to overloaded.

Additionally, I was curious to see if I could use AI to help me with the development of this bridge.

This add-on is a lightweight Go-based bridge connecting a CUL (USB serial or network TCP, 868MHz MAX! protocol) to Home Assistant via MQTT. It focuses on usage of an existing MAX! installation with wall thermostats and radiator valves, only supporting the really required functions to be used with Home Assistant. It's not intended as a full replacement of MAX! Cube or the implementation contained in FHEM.

## History

Who made what, newest first. Full list with dates: [CHANGELOG](max2mqtt/CHANGELOG.md).

**This fork, [dktzde/max2mqtt](https://github.com/dktzde/max2mqtt)** (since 2026-10-24):

- **v1.4.3** (2026-10-28) — Week profile messages are never discarded and keep a credit reserve for direct commands (`week_profile_reserve_percent`)
- **v1.4.2** (2026-10-28) — Week profile messages wait up to 120 minutes, Home Assistant notification after 60 minutes
- **v1.4.1** (2026-10-28) — Week profile messages wait up to 60 minutes for duty cycle credits
- **v1.4.0** (2026-10-28) — Drop-in replacement for the MAX! Cube integration: device data kept across restarts, Cube style climate attributes (valve_position, programme, ...), Cube presets (Window Open, On, none)
- **v1.3.7** (2026-10-28) — Unknown config values are no longer overwritten with defaults (import/set topics), week profile per day and in MAX! Cube format
- **v1.3.6** (2026-10-28) — Commands wait for duty cycle credits, bridge availability for all entities, Duty Cycle and TX Credits sensors
- **v1.3.5** (2026-10-28) — Fixes: preset after boost, boost no longer sets 30 °C, actual temperature 0 ignored, auto follows the week program
- **v1.3.4** (2026-10-26) — Vacation mode (temperature until date/time) via `max/<id>/vacation/set`, "Vacation Until" sensor
- **v1.3.3** (2026-10-25) — Auto-Ack (`Za`), valve position sensor, RF error sensor, shutter contacts (window/battery/RF error)

**[Dad-Ju/max2mqtt](https://github.com/Dad-Ju/max2mqtt)** (2026-10-15 to 2026-10-16):

- **v1.2.0** (2026-10-15) — Network CUL: `serial_port: tcp://host:port` (baud unused)
- **v1.3.0 to v1.3.2** (2026-10-16) — Full device configuration in HA (temps, valve, presets, week profile) with CUL Ack/push confirmation (no polling), SetTemperature fixes

**Original by [pwurbs/max2mqtt](https://github.com/pwurbs/max2mqtt)** (December 2025 to January 2026):

- **v1.0.0 and v1.1.0** — the bridge itself: CUL to MQTT, wall and radiator thermostats, duty cycle management, pairing and association

```yaml
serial_port: "tcp://192.168.1.50:2323"
baud_rate: 38400   # unused for TCP
mqtt_broker: "tcp://homeassistant.local:1883"
gateway_id: "0A1B2C"   # old Cube / FHEM address, see above
```

See [Add-On ReadMe](max2mqtt/README.md) and [Documentation](max2mqtt/DOCS.md).
