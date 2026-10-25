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

## What's new in this fork

Built on [Dad-Ju/max2mqtt](https://github.com/Dad-Ju/max2mqtt), originally [pwurbs/max2mqtt](https://github.com/pwurbs/max2mqtt):

- **v1.2.0** — Network CUL: `serial_port: tcp://host:port` (baud unused)
- **v1.3.0** — Full device configuration in HA (temps, valve, presets, week profile) with CUL Ack/push confirmation (no polling)
- **v1.3.3 (dktzde fork)** — Auto-Ack (`Za`), valve position sensor, RF error sensor, shutter contacts (window/battery/RF error)

```yaml
serial_port: "tcp://192.168.1.50:2323"
baud_rate: 38400   # unused for TCP
mqtt_broker: "tcp://homeassistant.local:1883"
gateway_id: "0A1B2C"   # old Cube / FHEM address, see above
```

See [Add-On ReadMe](max2mqtt/README.md) and [Documentation](max2mqtt/DOCS.md).
