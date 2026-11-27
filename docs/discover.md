# Discover a Zelio Logic relay over USB

[Читать по-русски](discover.ru.md)

Use `zelio discover` to inspect a running Zelio Logic application over USB and
prepare a read-only MQTT bridge configuration. The observed hardware is a
Schneider Electric **SR3B261FU** with the **SR3XT141FU** expansion and an
**SR2USB01** programming cable. The USB transport targets Linux, including
Raspberry Pi OS, and macOS. The supported cable identifies itself as USB
`16de:0084` with `bcdDevice=0600`.

## Connect the relay

1. Connect the Zelio end of the SR2USB01 cable to the relay's programming
   connector according to Schneider Electric's instructions. Connect the USB
   end to the host that will run `zelio`. The SR2USB01 has a dedicated Zelio
   plug. [Schneider Electric product page](https://www.se.com/uk/en/product/SR2USB01/usb-pc-connecting-cable-for-smart-relay-zelio-logic-3-m/)
   · [Schneider Electric cable FAQ](https://www.se.com/uk/en/faqs/FA23076/)
2. Build `zelio`. On Linux, install the udev rule, add your user to the `zelio`
   group and replug the USB end after the new group membership takes effect.
   On macOS, use a native libusb build and allow the accessory if prompted.
   [Build steps](../README.md#quick-start) · [Linux USB access](install.md#linux-usb-access)
   · [macOS setup](install.md#macos)
3. Stop the bridge and quit Zelio Soft and any other software using the
   cable. The tool needs exclusive access to the cable's USB interface
   through libusb. On Linux, check `lsusb -t` if the interface is busy.
   [USB troubleshooting](install.md#diagnose-a-connection)
4. Keep the relay in RUN and select the front-panel I/O page so the bridge
   can read logical states. Run `bin/zelio list` and copy the USB cable serial.
   If the same cable is moved to another relay, verify the application and
   I/O mapping on that relay.

## Generate a read-only configuration

Replace `SERIAL` with the USB cable serial, choose a unique MQTT device ID
and set the address of your MQTT 5 broker. The defaults use a device ID based
on the cable serial and a broker on the same host. Use environment variables
for broker credentials. [MQTT setup](install.md#mqtt-setup)

```sh
bin/zelio discover --serial SERIAL --device-id zelio_lights \
  --mqtt-url mqtt://127.0.0.1:1883 --config-out zelio.json
```

Discovery prints a JSON report to standard output, including the proposed
bridge `config`. `--config-out` saves that configuration to a new file and
refuses to overwrite an existing file. Keep the report locally if you want to
compare results after servicing the relay. It may contain identifiers and
information about your running application.

The generated configuration publishes wired `Q` outputs as read-only binary
sensors. Review the channel IDs, names and physical labels before serving it.
You can add read-only inputs using the same format. [Example configuration](../config.example.json)

`Q` labels indicate logical output state. Check the actual load or independent
feedback when you need to verify real actuation. Discovery reads data without
pulsing signals, forcing outputs, transferring a program or switching the
relay between RUN and STOP. [Protocol notes](protocol.md)

```sh
bin/zelio validate --config zelio.json
bin/zelio serve --config zelio.json
```

`validate` checks the configuration. Verify the labels on the running relay
and check the reflected state in Home Assistant. For long-running service
setup, follow the Linux systemd or macOS instructions. [Deployment guide](install.md)

## Review FBD control candidates

Candidate detection covers a limited subset of compiled FBD logic. A
`toggle_candidate` follows a recognized path through a bistable block's toggle
input. The `outputs` field lists the wired `Q` outputs, and `routes` describes
potential links between FBD signals and those outputs. Each route includes its
`signal_id`, `block` and classification `kind`. The physical `input`, affected
`outputs` and explanatory `reason` appear when available. `reset_outputs`
lists the outputs for which the decoder found an active-high path from a
physical reset input. A recognized program with wired outputs can yield a
read-only configuration even if it contains `reset` or `unresolved` routes.
Discovery exits with an error if it encounters an unsupported or incomplete
program image.

Compare `program_sha256` and `config.cable_serial` between reports. The
fingerprint is a SHA-256 digest of program data that matched across two USB
reads. `serve` checks the configuration's `fbd_sha256` against the connected
application on each cable session. A mismatch makes the device unavailable and
`serve` retries the connection. Repeat the review after an application change
or when moving the cable to another relay.

Review a `toggle_candidate` against the installed application and the physical
switch. For a group of track lights, watch which `Q` outputs and loads change
when the physical switch operates. Use the read-only bridge or the relay's I/O
page for the logical readings. Keep read-only operation if the route remains
unclear.

Review a master OFF link separately as a reset that can turn off several
outputs. If its `reset_outputs` overlap a selected group, discovery adds the
physical input to `block_when`. The bridge blocks commands while that input is
active. Check any uncertain reset paths separately.

## Test one toggle before deployment

Stop the running bridge and create a pilot configuration with one reviewed
route and a separate MQTT device ID. Set `--expect-program` to `program_sha256`
from the report you reviewed. Discovery requires a matching fingerprint to
create the control configuration. Replace `SIGNAL_ID` with the reported
`signal_id` in decimal or `0x`-prefixed hexadecimal. `--verified-signal`
explicitly allows the selected route in the configuration for the pilot.

```sh
bin/zelio discover --serial SERIAL --device-id zelio_pilot \
  --mqtt-url mqtt://127.0.0.1:1883 --expect-program PROGRAM_SHA256 \
  --verified-signal SIGNAL_ID --config-out zelio-pilot.json
bin/zelio validate --config zelio-pilot.json
```

Review the selected `Q` labels and any `block_when` inputs in
`zelio-pilot.json` before starting the bridge.

```sh
bin/zelio serve --config zelio-pilot.json
```

Use the pilot switch in Home Assistant to command one transition at a time.
Check the logical readback and the actual loads after each command. The
intended `Q` outputs should change together and unrelated outputs should keep
their state. Verify both ON and OFF, then restore the starting state and stop
the pilot bridge. Retain the read-only configuration for any route that
fails the check. [Home Assistant setup](../README.md#home-assistant)

After each selected route passes its pilot, generate a fresh configuration
using the production device ID and a new file. Repeat `--verified-signal`
for each tested route. The selected routes must affect disjoint sets of `Q`
outputs. Validate the file and deploy it. [Momentary control setup](momentary-control.md#configure-the-bridge)

If discovery fails or the bridge shows unavailable channels, check the cable
revision, controller RUN status and front-panel I/O page. Use
`bin/zelio probe --serial SERIAL` to check the cable and
`bin/zelio monitor-io --serial SERIAL` to check logical I/O.
[Connection checks](install.md#diagnose-a-connection)
