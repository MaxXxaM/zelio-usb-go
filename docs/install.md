# Install and operate

Build the CLI using the [quick start](../README.md#quick-start). Run USB
commands on the machine with the cable. For Home Assistant OS, run the bridge
on a separate Linux or macOS host with access to the same MQTT broker.

## Linux USB access

Install the udev rule and grant your interactive user access.

```sh
sudo groupadd -f zelio
sudo usermod -aG zelio "$(id -un)"
sudo install -m 0644 deploy/70-zelio-usb.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules
```

Log out and back in for the new group to take effect. Replug the USB end and
run `bin/zelio list`. If the interface is busy, stop the existing bridge or
Zelio Soft. Check `lsusb -t` for a bound serial driver. If your system binds a
driver to this cable, unbind that specific interface before starting the bridge.
The USB transport claims interface 0 through libusb and leaves kernel-driver
bindings under the operator's control.

## Linux systemd

The template runs as a dedicated, unprivileged `zelio` user. This example
installs an instance named `bench`.

```sh
id zelio >/dev/null 2>&1 || sudo useradd --system --gid zelio --no-create-home --shell /usr/sbin/nologin zelio
sudo install -m 0755 bin/zelio /usr/local/bin/zelio
sudo install -d -m 0750 -o root -g zelio /etc/zelio
sudo install -m 0640 -o root -g zelio config.json /etc/zelio/bench.json
sudo install -m 0644 deploy/zelio@.service /etc/systemd/system/
sudo systemctl daemon-reload
```

If your configuration refers to credential environment variables, create
`/etc/zelio/bench.env` as a root-owned file with mode `0600`. Use one
`VARIABLE=value` entry per line with your broker credentials.
A private broker CA can be set with `mqtt.ca_file`. Store that PEM where the
service user can read it, for example in `/etc/zelio`.

```sh
sudo systemctl start zelio@bench
sudo journalctl -u zelio@bench -f
```

For read-only operation, `sudo systemctl enable zelio@bench` enables startup
at boot. For a force-enabled configuration, start the service manually after
verifying the PLC connection. The template leaves process restarts manual.
The running process recovers broker and USB connections internally.

## macOS

Use Xcode Command Line Tools and a native Homebrew installation. On Apple
Silicon, `file "$(brew --prefix libusb)/lib/libusb-1.0.dylib"` should report
arm64. If `pkg-config` finds another installation first, select the Homebrew
library for the build:

```sh
PKG_CONFIG_PATH="$(brew --prefix libusb)/lib/pkgconfig" \
  go build -o bin/zelio ./cmd/zelio
bin/zelio serve --config config.json
```

Keep the bridge in the foreground initially and stop it with Ctrl-C. For a
read-only background instance, use a user LaunchAgent with absolute paths to
`zelio` and the config, and load it into the logged-in user's session. Supply
the configured credential environment variables through a private launcher or
your deployment's secret store. USB access may require allowing the accessory
in macOS. Quit software that has already claimed the cable.

## MQTT setup

The broker must support MQTT 5, retained discovery, and QoS 1 state publishing.
Home Assistant uses the broker independently and can publish switch commands
at QoS 0 with retain disabled. The bridge supports `mqtts://` with host
certificate validation and `mqtt://` for a trusted local connection. Use
`mqtt.ca_file` for a private CA or leave it unset for the system CA pool.
Credentials come from environment variables.

Give the bridge its own broker account. For device ID `bench_relay` and default
discovery prefix, grant publish access to `zelio/bench_relay/#`,
`homeassistant/binary_sensor/bench_relay/#` and
`homeassistant/switch/bench_relay/#`. For experimental control, grant it
subscription access to `zelio/bench_relay/command/+/+`. Restrict command
publication to the intended controller account.

| Topic | Payload |
| --- | --- |
| `zelio/<device>/<channel>/state` | Retained `ON` or `OFF`. Unavailability clears the retained state. |
| `zelio/<device>/<channel>/availability` | Retained `online` or `offline` for the logical reading. |
| `zelio/<device>/<channel>/control` | Retained `online` or `offline` for an armed switch. |
| `zelio/<device>/availability` | Retained `online/<session token>`, with an `offline` Last Will. |
| `zelio/<device>/command/<session token>/<channel>` | Live `ON` or `OFF` for an explicitly enabled control binding. Read the active topic from discovery. |

The bridge uses a clean session with expiry zero and rotates command topics
on every connection. MQTT 5 retain handling suppresses delivery of previously
retained commands and preserves the retention bit for the live-command filter.
Commands are accepted only while the PLC loop is ready. A busy loop can drop
a command. Home Assistant waits for a state update after issuing one.
The Last Will depends on broker operation and keepalive. After a broker crash,
a broker using persistence can briefly restore an old retained view until the
next bridge connection publishes fresh availability.

When renaming or removing a channel, publish an empty retained payload to its
former discovery topic. For example:

```sh
mosquitto_pub -h broker.example -t homeassistant/binary_sensor/bench_relay/old_input/config -r -n
```

Use your broker's authentication and TLS options for that command. The new
session makes old discovery unavailable. Clearing the old discovery removes
the entity from Home Assistant.

## Diagnose a connection

| Symptom | Check |
| --- | --- |
| Cable absent from `list` | Check USB access, permissions and that the cable advertises `16de:0084`. |
| Unsupported USB revision | Confirm the descriptor. The transport implements `0600`. |
| USB claim busy | Stop other software using the interface and check driver ownership. |
| `probe` works, but PLC reads time out | `probe` reads the cable registers. Check the PLC-side connection, RUN and the configured timeout. |
| I/O unavailable | Select the PLC's RUN I/O page. Some other pages use the same read address. |
| Controlled switch unavailable after reconnect | Read the logical state topic. Verify the cable still connects to the intended PLC before restarting the bridge. For legacy Q1 control, check any held force. |
| Command outcome uncertain | Inspect the affected outputs before further commands. With legacy force control, also inspect the force state in Zelio Soft. |

`uart-check` configures the cable UART and toggles DTR/RTS as requested. It
sends zero UART bytes to the PLC. Treat diagnostic output containing serials
or raw program data as local information.
