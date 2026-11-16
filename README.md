# Zelio USB for Go

Read Schneider Electric Zelio Logic I/O through an SR2USB01 programming cable
on Linux or macOS. The CLI provides USB diagnostics, and the MQTT 5 bridge
publishes named logical states to Home Assistant. Monitoring runs alongside the
PLC's existing program.

| Setup | Verified behavior |
| --- | --- |
| SR2USB01 `16de:0084`, device revision `0600`, with SR3B261FU + SR3XT141FU in RUN on macOS ARM64 | On one physical setup, the original Go driver read the front-panel I/O and SL OUT. Force OFF and Force ON for Q1 changed a real load. The bench sequence included a global release between the two forces and individual Release ON at the end. |
| macOS ARM64 and Linux ARM64 | The public project builds and runs its software tests on both architectures. The MQTT path is tested against Mosquitto with a simulated PLC. |
| Other programs, outputs, cable revisions and the Linux USB path | Hardware validation is pending. The Q1 signal ID depends on the exact PLC program. |

## Quick start

Install **Go 1.24+**, a C compiler, `pkg-config` and libusb 1.0 for the host
architecture. On Raspberry Pi OS 64-bit or Debian, install the development
package. On macOS, use Homebrew matching the architecture reported by
`go env GOARCH`.

```sh
# Debian / Raspberry Pi OS
sudo apt-get install build-essential pkg-config libusb-1.0-0-dev

# macOS
xcode-select --install
brew install libusb pkgconf
```

```sh
git clone https://github.com/MaxXxaM/zelio-usb-go.git
cd zelio-usb-go
go build -o bin/zelio ./cmd/zelio
bin/zelio list
bin/zelio probe --serial SERIAL
bin/zelio monitor-io --serial SERIAL
```

Use the cable serial from `list`. On Linux, apply the
[udev rule](docs/install.md#linux-usb-access) to run as a regular user. Follow the
controller's instructions for connecting the PLC-side plug. Keep the relay in
RUN and display its I/O page. If the page is unavailable, the CLI reports
`available: false`.

### Home Assistant

1. Set up an MQTT 5 broker and enable the
   [MQTT integration](https://www.home-assistant.io/integrations/mqtt/) in Home Assistant.
2. Copy [config.example.json](config.example.json) to `config.json`. Set the
   broker URL, cable serial, device ID and channel names. Each channel maps an
   `I` or `Q` label such as `I1` or `Q1` to a stable ID and display name.
   Verify each label against the front panel.
3. Set the credential environment variables named in the config, then start
   the bridge.

```sh
bin/zelio validate --config config.json
bin/zelio serve --config config.json
```

The example publishes read-only binary sensors. When the I/O page is absent,
the relay leaves RUN, or USB communication fails, the corresponding channels
become unavailable. Q represents **logical** output state. Verifying voltage
or load operation requires independent feedback.

Run one instance per PLC and cable, each with a unique device ID. See
[installation and troubleshooting](docs/install.md) for Linux systemd,
macOS setup, and how to remove retired MQTT entities.

### Experimental Q1 control

For a bench installation where you have independently confirmed **Q1 =
Monitoring signal `0x2A` on the exact program**, add the following field to the
Q1 channel:

```json
"control": "verified-q1-force-maintain"
```

The bridge exposes that channel as a Home Assistant switch. It reads Q1 in RUN
before sending any force, skips a force if Q1 is already at the requested
value, and reads Q1 again to confirm the logical result. ON and OFF **hold** the
signal. A force can survive USB unplug, bridge shutdown or a lost reply, and can
override local controls. Directly reversing an existing held force has only
been exercised with a simulated PLC.

After a lost session, the bridge resumes reading and disarms control. Verify
the cable is still attached to the intended PLC before restarting the service
to arm it again. The USB serial belongs to the cable. Stop the service before
moving the cable between controllers or using Zelio Soft.

To inspect Q1 or issue a deliberate manual action, stop the bridge first:

```sh
bin/zelio monitor-io --serial SERIAL
bin/zelio force-q1 --serial SERIAL --state ON --confirm-mapping --apply
bin/zelio release-q1 --serial SERIAL --confirm-mapping --apply
```

Individual release through this CLI requires Q1 to be logically ON. Use
Zelio Soft to inspect and manage other held forces. See the
[protocol notes](docs/protocol.md) for the experiment, the supported commands,
and the implications of holding a signal.

## Go API

The root `zelio` package accepts a `Transport` that reads and writes UART bytes.
`sr2usb` is the optional libusb transport. The `bridge` package handles
polling, logical state and command validation through interfaces for a device and
state publisher. The CLI's MQTT adapter implements the first integration.

```go
cable, err := sr2usb.Open(serial, sr2usb.UARTOptions{DTR: true, RTS: true})
if err != nil {
    return err
}
plc, err := zelio.NewClient(cable, zelio.Options{Timeout: time.Second})
if err != nil {
    cable.Close()
    return err
}
defer plc.Close()

view, err := plc.ReadMonitoringIO(ctx)
if err != nil {
    return err
}
on, available := view.Lookup("Q1")
fmt.Printf("Q1 logical state: on=%t available=%t\n", on, available)
```

A transport or frame error can desynchronize the session. Close the client and
reopen the cable before reading again. A failed write can have an uncertain
outcome. Check Q1 and the PLC's forces before deciding what to do next.

## Tests

```sh
go vet ./...
go test -race ./...
# Run the MQTT integration test against a disposable MQTT 5 broker.
ZELIO_TEST_BROKER=127.0.0.1:18883 go test -race -count=1 ./internal/mqttbridge
```

GitHub Actions runs native Linux and macOS ARM64 builds. The command links
against the platform's libusb. Source builds use the libusb installed on the
target host.

[MIT license](LICENSE). Independent project for Schneider Electric Zelio Logic.
