# Protocol and evidence

SR2USB01 with VID/PID `16de:0084` and `bcdDevice=0600` uses FT232R-style
USB control requests, 115200 7E1 and a two-byte FTDI status prefix per USB IN
packet. The adapter claims USB interface 0, purges the cable UART and applies
DTR/RTS at open. UART framing, parity and break errors abort the read. The
transport supports Linux and macOS via libusb.

Zelio Monitoring uses an ASCII-hex envelope with a colon prefix, CRLF suffix
and two's-complement LRC. The following areas were observed with Zelio Soft
2 V5.4.2 and checked with the original Go driver on one SR3B261FU with
SR3XT141FU in RUN:

| Region | Observed operation |
| --- | --- |
| `0x6B` | Reads a two-byte status. `81 00` was observed in RUN with pulse writes accepted but ineffective. `83 00` was observed with working pulse control. |
| `0x6C` | The captured initialization write uses two bytes, `04 00`, followed by a successful ACK and later status `83 00`. |
| `0x6F` | Reads the 78-byte front-panel page. The I/O layout carries a RUN marker and up to 24 I and 16 Q positions on the observed setup. Other screens may return unavailable. |
| `0x70` | Reads 246 raw bytes of FBD data. Byte index 5, bit mask `0x04`, followed the tested Q1 in that program. The bridge uses the I/O page and does not derive a map from this byte. |
| `0x78` | Writes a four-byte FBD pulse, `E0 <signal ID> 00 01`. The corresponding click has no release frame. |
| `0x79` | Writes a 30-byte Monitoring force/release record. The write reply acknowledges the record. The logical result comes from a fresh I/O read. |

The bridge gates the initialization by the observed status and a valid RUN
snapshot. It checks the new status and output state before sending an
impulse. Project transfer and global release take no part in this sequence.
See the [mapping and recovery guide](momentary-control.md).

## Physical Q1 experiment

The test on one FBD program sent **Force OFF**, **Release All**, **Force ON**
and **individual Release ON** in order. The operator observed the attached
load turn off and on. After individual Release ON, Q1 still read ON. The
program stayed in RUN throughout the experiment.

Force uses prefix `E0 2A <value>`. Individual Release ON uses `00 2A 01`.
Release All uses a zero-filled record and clears every force on the PLC. The
client sends only Force ON, Force OFF and individual Release ON for signal
`0x2A` with a caller's explicit enablement. The CLI and bridge also require
a fresh RUN observation for Q1, and the CLI requires Q1 ON before individual
release. Offline encoders reproduce protocol vectors without USB traffic.

The numeric relation between physical Q1 and signal `0x2A` is specific to
the tested program. Independent release at OFF, replacement of an active
force with another force, other outputs, and continuous use of the bridge
need hardware validation. The driver leaves forces in place across fault
recovery. Schneider's programming guide describes how Monitoring and forcing
can affect PLC cycle time.

## Documented SL words

SL reads use the checksum `1 - sum` described by Schneider. Word addresses
are 1–48. SL OUT words carry data published by the running FBD program. The
test on the physical relay read SL OUT words 25–48. Word writes are available
as a frame encoder for documented addresses 1–24. Writing PLC programs,
changing RUN/STOP and updating firmware are outside this driver's scope.

## References

- [Schneider Zelio Logic Programming Guide, EIO0000002612](https://iportal.se.com/Contents/docs/Zelio%20Logic%20Programming%20Guide.pdf)
- [Schneider SL IN and SL OUT applicability](https://www.se.com/nl/en/faqs/FAQ000233111/)
- [Linux FTDI protocol definitions](https://github.com/torvalds/linux/blob/master/drivers/usb/serial/ftdi_sio.h)
- [libusb interface claiming](https://libusb.sourceforge.io/api-1.0/group__libusb__dev.html)
- [MQTT 5 standard](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html)
- [Home Assistant MQTT switch](https://www.home-assistant.io/integrations/switch.mqtt/)
