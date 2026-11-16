package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
	"github.com/MaxXxaM/zelio-usb-go/bridge"
	"github.com/MaxXxaM/zelio-usb-go/internal/config"
	"github.com/MaxXxaM/zelio-usb-go/internal/mqttbridge"
	"github.com/MaxXxaM/zelio-usb-go/sr2usb"
)

const help = `Usage: zelio COMMAND [OPTIONS]

  list
  probe --serial SERIAL
  uart-check --serial SERIAL
  monitor-io --serial SERIAL
  monitor-raw --serial SERIAL
  read-sl --serial SERIAL --word 25 [--count 1]
  validate --config FILE
  serve --config FILE
  force-q1 --serial SERIAL --state ON|OFF --confirm-mapping --apply
  release-q1 --serial SERIAL --confirm-mapping --apply
  encode-monitor-force --id 0x2a --kind force --state ON
  encode-sl-write --word 3 --values 8569

Force holds Q1 in its requested state. Release is verified only for Q1 ON.
Both actions require verification of Q1 = 0x2A on the exact PLC program.
The encode commands perform no USB I/O. SL words contain program data.
UART commands accept --timeout 1s, --dtr=true and --rts=true.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "zelio:", err)
		os.Exit(1)
	}
}

func output(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, help)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	parse := func() error {
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		return nil
	}
	switch command {
	case "validate", "serve":
		path := fs.String("config", "config.json", "configuration file")
		if err := parse(); err != nil {
			return err
		}
		cfg, err := config.Read(*path)
		if err != nil {
			return err
		}
		if command == "validate" {
			return output(out, map[string]any{
				"valid": true, "device_id": cfg.DeviceID,
				"channels": len(cfg.Channels), "control_enabled": cfg.HasControl(),
			})
		}
		open := func(ctx context.Context) (bridge.Device, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			wire, err := sr2usb.Open(cfg.CableSerial, sr2usb.UARTOptions{DTR: true, RTS: true})
			if err != nil {
				return nil, err
			}
			return client(wire, cfg.Timeout(), cfg.HasControl())
		}
		return mqttbridge.Run(ctx, cfg, open, slog.Default())
	case "encode-sl-write":
		word := fs.Int("word", 0, "first SL IN word")
		values := fs.String("values", "", "comma-separated decimal uint16 values")
		if err := parse(); err != nil {
			return err
		}
		var words []uint16
		for _, part := range strings.Split(*values, ",") {
			value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 16)
			if err != nil {
				return fmt.Errorf("invalid uint16 value %q", part)
			}
			words = append(words, uint16(value))
		}
		frame, err := zelio.WriteRequest(*word, words)
		if err != nil {
			return err
		}
		return output(out, map[string]any{"frame_ascii": string(frame), "usb_accessed": false})
	case "encode-monitor-force":
		kind := fs.String("kind", "force", "force, release, or release-all")
		id := fs.String("id", "0", "numeric Monitoring signal ID")
		state := fs.String("state", "", "ON or OFF")
		if err := parse(); err != nil {
			return err
		}
		frame, err := encodeMonitoring(*kind, *id, *state)
		if err != nil {
			return err
		}
		return output(out, map[string]any{"frame_ascii": string(frame), "usb_accessed": false})
	case "list":
		if err := parse(); err != nil {
			return err
		}
		items, err := sr2usb.List()
		if err != nil {
			return err
		}
		return output(out, items)
	case "probe":
		serial := fs.String("serial", "", "USB cable serial")
		if err := parse(); err != nil {
			return err
		}
		result, err := sr2usb.Probe(*serial)
		if err != nil {
			return err
		}
		return output(out, result)
	case "uart-check", "monitor-io", "monitor-raw", "read-sl", "force-q1", "release-q1":
		return uartCommand(ctx, command, fs, parse, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func uartCommand(ctx context.Context, command string, fs *flag.FlagSet, parse func() error, out io.Writer) error {
	serial := fs.String("serial", "", "USB cable serial")
	timeout := fs.Duration("timeout", time.Second, "PLC transaction timeout")
	dtr := fs.Bool("dtr", true, "assert DTR")
	rts := fs.Bool("rts", true, "assert RTS")
	var word, count int
	if command == "read-sl" {
		fs.IntVar(&word, "word", 0, "first SL word address")
		fs.IntVar(&count, "count", 1, "word count")
	}
	control := command == "force-q1" || command == "release-q1"
	var apply, confirmed bool
	if control {
		fs.BoolVar(&apply, "apply", false, "send a live PLC action")
		fs.BoolVar(&confirmed, "confirm-mapping", false, "Q1 = 0x2A verified on this PLC program")
	}
	var state string
	if command == "force-q1" {
		fs.StringVar(&state, "state", "", "ON or OFF")
	}
	if err := parse(); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if command == "read-sl" {
		if _, err := zelio.ReadRequest(word, count); err != nil {
			return err
		}
	}
	var kind zelio.MonitorForceKind
	var on bool
	if control {
		if !apply || !confirmed || *serial == "" {
			return errors.New("a live action requires --apply, --confirm-mapping and --serial")
		}
		if command == "force-q1" {
			kind = zelio.MonitorForce
			if state != "ON" && state != "OFF" {
				return errors.New("--state must be ON or OFF")
			}
			on = state == "ON"
		} else {
			kind, on = zelio.MonitorRelease, true
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	wire, err := sr2usb.Open(*serial, sr2usb.UARTOptions{DTR: *dtr, RTS: *rts})
	if err != nil {
		return err
	}
	if command == "uart-check" {
		defer wire.Close()
		status, err := wire.Status()
		if err != nil {
			return err
		}
		return output(out, map[string]any{"baud": 115200, "format": "7E1", "dtr": *dtr, "rts": *rts, "status": status, "plc_bytes_sent": 0})
	}
	c, err := client(wire, *timeout, control)
	if err != nil {
		return err
	}
	defer c.Close()
	switch command {
	case "monitor-io":
		io, err := c.ReadMonitoringIO(ctx)
		if err != nil {
			return err
		}
		return output(out, io)
	case "monitor-raw":
		data, err := c.ReadMonitoringRawFBD(ctx)
		if err != nil {
			return err
		}
		return output(out, map[string]any{"region": "0x70", "bytes_hex": hex.EncodeToString(data), "decoded": false})
	case "read-sl":
		words, err := c.ReadWords(ctx, word, count)
		if err != nil {
			return err
		}
		return output(out, map[string]any{"word": word, "values": words})
	default:
		if _, err := bridge.ActuateQ1(ctx, c, kind, on); err != nil {
			return err
		}
		return output(out, map[string]any{"label": "Q1", "logical_on": on, "source": "RUN I/O readback"})
	}
}

func client(wire zelio.Transport, timeout time.Duration, control bool) (*zelio.Client, error) {
	c, err := zelio.NewClient(wire, zelio.Options{Timeout: timeout, AllowMonitoringWrites: control})
	if err != nil {
		_ = wire.Close()
	}
	return c, err
}

func encodeMonitoring(kind, id, state string) ([]byte, error) {
	switch kind {
	case "release-all":
		return zelio.MonitoringForceRequest(0, zelio.MonitorReleaseAll, false)
	case "force", "release":
		signalID, err := strconv.ParseUint(id, 0, 8)
		if err != nil {
			return nil, fmt.Errorf("invalid signal ID %q", id)
		}
		if state != "ON" && state != "OFF" {
			return nil, errors.New("--state must be ON or OFF")
		}
		k := zelio.MonitorForce
		if kind == "release" {
			k = zelio.MonitorRelease
		}
		return zelio.MonitoringForceRequest(uint8(signalID), k, state == "ON")
	default:
		return nil, fmt.Errorf("unknown Monitoring frame kind %q", kind)
	}
}
