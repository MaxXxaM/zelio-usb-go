// Package mqttbridge publishes bridge observations and accepts session-scoped
// Home Assistant commands over MQTT 5.
package mqttbridge

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/MaxXxaM/zelio-usb-go/bridge"
	"github.com/MaxXxaM/zelio-usb-go/internal/config"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

const (
	packetTimeout         = 4 * time.Second
	commandHandoffTimeout = 200 * time.Millisecond
)

type broker struct {
	address string
	tls     *tls.Config
	user    string
	pass    string
}

func brokerConfig(cfg config.MQTT) (broker, error) {
	u, err := cfg.BrokerURL()
	if err != nil {
		return broker{}, err
	}
	password, err := readEnv(cfg.PasswordEnv)
	if err != nil {
		return broker{}, err
	}
	username, err := readEnv(cfg.UsernameEnv)
	if err != nil {
		return broker{}, err
	}
	b := broker{address: address(u), user: username, pass: password}
	if u.Scheme == "mqtts" {
		b.tls = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
		if cfg.CAFile != "" {
			cert, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return broker{}, fmt.Errorf("read MQTT CA: %w", err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(cert) {
				return broker{}, errors.New("MQTT CA file does not contain a PEM certificate")
			}
			b.tls.RootCAs = roots
		}
	}
	return b, nil
}

func address(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "1883"
		if u.Scheme == "mqtts" {
			port = "8883"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func readEnv(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("set the MQTT credential environment variable %s", name)
	}
	return value, nil
}

func (b broker) dial(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: packetTimeout}
	if b.tls != nil {
		return (&tls.Dialer{NetDialer: dialer, Config: b.tls}).DialContext(ctx, "tcp", b.address)
	}
	return dialer.DialContext(ctx, "tcp", b.address)
}

type boundedWriter struct{ net.Conn }

func (c boundedWriter) Write(data []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(packetTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(data)
}

func decodeCommand(p *paho.Publish, topics map[string]string) (bridge.Command, bool) {
	id, found := topics[p.Topic]
	value := string(p.Payload)
	if !found || p.Retain || p.Duplicate() || p.QoS != 0 || (value != "ON" && value != "OFF") {
		return bridge.Command{}, false
	}
	return bridge.Command{ID: id, On: value == "ON"}, true
}

// handoffCommand lets a short poll finish without saving the command for a
// later session. The receiver must accept it before the deadline.
func handoffCommand(ctx context.Context, commands chan<- bridge.Command, cmd bridge.Command) bool {
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(commandHandoffTimeout)
	defer timer.Stop()
	select {
	case commands <- cmd:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

// Run starts a new clean MQTT session after each failure. The bridge's USB
// session and command receiver are scoped to that connection.
func Run(ctx context.Context, cfg config.File, open bridge.Open, log *slog.Logger) error {
	b, err := brokerConfig(cfg.MQTT)
	if err != nil {
		return err
	}
	if log == nil {
		log = slog.Default()
	}
	arm := cfg.HasControl()
	for ctx.Err() == nil {
		err := session(ctx, b, cfg, open, &arm, log)
		if ctx.Err() != nil {
			break
		}
		log.Warn("MQTT session ended", "error", err)
		timer := time.NewTimer(cfg.Settings().RetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

func session(parent context.Context, b broker, cfg config.File, open bridge.Open, arm *bool, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(random[:])
	topics := make(map[string]string)
	var subs []paho.SubscribeOptions
	for _, binding := range cfg.Channels {
		if *arm && binding.CanControl() {
			topic := commandTopic(cfg.DeviceID, nonce, binding.ID)
			topics[topic] = binding.ID
			subs = append(subs, paho.SubscribeOptions{
				Topic: topic, QoS: 0, RetainHandling: 2, RetainAsPublished: true,
			})
		}
	}
	commands := make(chan bridge.Command)
	dialCtx, finishDial := context.WithTimeout(ctx, packetTimeout)
	conn, err := b.dial(dialCtx)
	finishDial()
	if err != nil {
		return err
	}
	defer conn.Close()
	client := paho.NewClient(paho.ClientConfig{
		Conn:               packets.NewThreadSafeConn(boundedWriter{conn}),
		PacketTimeout:      packetTimeout,
		OnServerDisconnect: func(*paho.Disconnect) { cancel() },
		OnClientError:      func(error) { cancel() },
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){
			func(msg paho.PublishReceived) (bool, error) {
				cmd, accepted := decodeCommand(msg.Packet, topics)
				if !accepted || ctx.Err() != nil {
					return false, nil
				}
				if !handoffCommand(ctx, commands, cmd) {
					log.Warn("dropping MQTT command while PLC is busy or unavailable", "channel", cmd.ID)
				}
				return true, nil
			},
		},
	})
	link := linkTopic(cfg.DeviceID)
	sessionExpiry := uint32(0)
	maxPacket := uint32(64 << 10)
	_, err = client.Connect(ctx, &paho.Connect{
		ClientID:   "zelio-" + cfg.DeviceID,
		CleanStart: true,
		KeepAlive:  15,
		Username:   b.user, UsernameFlag: b.user != "",
		Password: []byte(b.pass), PasswordFlag: b.pass != "",
		Properties: &paho.ConnectProperties{
			SessionExpiryInterval: &sessionExpiry,
			MaximumPacketSize:     &maxPacket,
		},
		WillMessage: &paho.WillMessage{Topic: link, Payload: []byte("offline"), QoS: 1, Retain: true},
	})
	if err != nil {
		return fmt.Errorf("MQTT connect: %w", err)
	}
	defer func() {
		// A failed offline publication leaves the socket to close without
		// DISCONNECT, triggering the broker's retained offline Will.
		stop, done := context.WithTimeout(context.Background(), packetTimeout)
		defer done()
		if publish(stop, client, link, []byte("offline")) == nil {
			_ = client.Disconnect(&paho.Disconnect{})
		}
	}()
	if err := publish(ctx, client, link, []byte("offline")); err != nil {
		return err
	}
	if len(subs) > 0 {
		ack, err := client.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs})
		if err != nil {
			return fmt.Errorf("MQTT command subscription: %w", err)
		}
		if len(ack.Reasons) != len(subs) {
			return errors.New("unexpected MQTT command SUBACK")
		}
		for _, reason := range ack.Reasons {
			if reason != 0 {
				return fmt.Errorf("MQTT command subscription rejected: 0x%02x", reason)
			}
		}
	}
	sink := stateSink{
		client: client, device: cfg.DeviceID,
		controls: make(map[string]bool),
		last:     make(map[string]bridge.State),
	}
	initial := make([]bridge.State, 0, len(cfg.Channels))
	for _, binding := range cfg.Channels {
		initial = append(initial, bridge.State{ID: binding.ID})
		sink.controls[binding.ID] = binding.CanControl()
	}
	if err := sink.report(ctx, initial); err != nil {
		return err
	}
	for _, binding := range cfg.Channels {
		kind := component(binding)
		other := "switch"
		if kind == other {
			other = "binary_sensor"
		}
		prefix := cfg.MQTT.DiscoveryPrefix
		if err := publish(ctx, client, configTopic(prefix, other, cfg.DeviceID, binding.ID), nil); err != nil {
			return err
		}
		payload, err := json.Marshal(discovery(cfg.DeviceID, cfg.DeviceName, nonce, binding))
		if err != nil {
			return err
		}
		if err := publish(ctx, client, configTopic(prefix, kind, cfg.DeviceID, binding.ID), payload); err != nil {
			return err
		}
	}
	if err := publish(ctx, client, link, []byte("online/"+nonce)); err != nil {
		return err
	}
	log.Info("MQTT connected", "device", cfg.DeviceID)
	settings := cfg.Settings()
	settings.ControlArmed = *arm
	*arm = false
	if err := bridge.Run(ctx, settings, open, commands, sink.report, log); err != nil {
		return err
	}
	if parent.Err() == nil {
		if settings.ControlArmed {
			log.Warn("PLC control disarmed; verify the PLC connection before restarting")
		}
		return errors.New("broker disconnected")
	}
	return nil
}

type stateSink struct {
	client   *paho.Client
	device   string
	controls map[string]bool
	last     map[string]bridge.State
}

func (s *stateSink) report(ctx context.Context, states []bridge.State) error {
	for _, state := range states {
		prior, known := s.last[state.ID]
		if known && prior == state {
			continue
		}
		topic := channelTopic(s.device, state.ID)
		if !state.Available {
			if err := publish(ctx, s.client, topic+"/availability", []byte("offline")); err != nil {
				return err
			}
			if err := publish(ctx, s.client, topic+"/state", nil); err != nil {
				return err
			}
		} else {
			value := "OFF"
			if state.On {
				value = "ON"
			}
			if err := publish(ctx, s.client, topic+"/state", []byte(value)); err != nil {
				return err
			}
			if !known || !prior.Available {
				if err := publish(ctx, s.client, topic+"/availability", []byte("online")); err != nil {
					return err
				}
			}
		}
		if s.controls[state.ID] && (!known || prior.ControlReady != state.ControlReady) {
			availability := "offline"
			if state.ControlReady {
				availability = "online"
			}
			if err := publish(ctx, s.client, topic+"/control", []byte(availability)); err != nil {
				return err
			}
		}
		s.last[state.ID] = state
	}
	return nil
}

func publish(ctx context.Context, client *paho.Client, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ack, err := client.Publish(ctx, &paho.Publish{Topic: topic, QoS: 1, Retain: true, Payload: payload})
	if err != nil {
		return fmt.Errorf("MQTT publish %s: %w", topic, err)
	}
	if ack.ReasonCode >= 0x80 {
		return fmt.Errorf("MQTT publish %s rejected: 0x%02x", topic, ack.ReasonCode)
	}
	return nil
}
