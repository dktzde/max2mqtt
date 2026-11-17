package main

import (
	"log/slog"
	"sync"
)

// Devices are created only for telegrams the bridge can trust. Seen 2026-11-17: a MAX! Cube with
// a-culfw delivered garbled lines for eight minutes, and the bridge created seven climate entities
// from fragments of real addresses (with setpoints such as 57.5 °C).
//
// A telegram from a device the bridge does not know yet creates it only when
//   - it is addressed to the gateway (a device paired with the bridge or with the Cube whose
//     address is gateway_id sends its Acks, time requests and window states there), or
//   - the device was paired with the bridge since the start (PairPong sent).
// Known devices (in devices.json, or seen since the start) are handled as before. The option
// accept_unknown_devices: true creates every device heard, as before (e.g. thermostats of a
// neighbour, or devices linked only to each other).

var (
	pairedMu      sync.Mutex
	pairedDevices = map[string]bool{}
	// unknownLogged: unknown devices already logged once since the start
	unknownLogged = map[string]bool{}
)

// markPaired records a device the bridge sent a PairPong to
func markPaired(deviceID string) {
	pairedMu.Lock()
	pairedDevices[deviceID] = true
	pairedMu.Unlock()
}

// isKnownDevice reports whether the bridge has a state, config or window contact for the device
func isKnownDevice(deviceID string) bool {
	stateMutex.RLock()
	_, ok := stateCache[deviceID]
	stateMutex.RUnlock()
	if ok {
		return true
	}
	configMutex.RLock()
	_, ok = deviceConfigCache[deviceID]
	configMutex.RUnlock()
	if ok {
		return true
	}
	shutterMu.Lock()
	ok = shutterDiscovered[deviceID]
	shutterMu.Unlock()
	if ok {
		return true
	}
	pairedMu.Lock()
	defer pairedMu.Unlock()
	return pairedDevices[deviceID]
}

// deviceAccepted reports whether a state telegram may create or update the device of pkt.SrcAddr
func deviceAccepted(pkt *MaxPacket) bool {
	if config.AcceptUnknownDevices || pkt.DstAddr == gatewayAddrHex() || isKnownDevice(pkt.SrcAddr) {
		return true
	}
	pairedMu.Lock()
	first := !unknownLogged[pkt.SrcAddr]
	unknownLogged[pkt.SrcAddr] = true
	pairedMu.Unlock()
	if first {
		slog.Info("Ignored telegrams from an unknown device (not paired with the bridge, not addressed to the gateway); pair it, or set accept_unknown_devices",
			"src", pkt.SrcAddr, "dst", pkt.DstAddr, "type", pkt.Type)
	}
	return false
}
