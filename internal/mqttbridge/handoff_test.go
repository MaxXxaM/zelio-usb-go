package mqttbridge

import (
	"context"
	"testing"
	"time"

	"github.com/MaxXxaM/zelio-usb-go/bridge"
)

func TestCommandHandoffWaitsOnlyForLiveReceiver(t *testing.T) {
	commands := make(chan bridge.Command)
	want := bridge.Command{ID: "q1", On: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := make(chan bridge.Command, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		select {
		case c := <-commands:
			got <- c
		case <-ctx.Done():
		}
	}()
	if !handoffCommand(ctx, commands, want) {
		t.Fatal("short poll prevented handoff")
	}
	if received := <-got; received != want {
		t.Fatalf("got %+v, want %+v", received, want)
	}
	if handoffCommand(ctx, commands, want) {
		t.Fatal("command accepted with no receiver")
	}
	select {
	case <-commands:
		t.Fatal("expired command was queued")
	default:
	}
	cancel()
	if handoffCommand(ctx, commands, want) {
		t.Fatal("command accepted after session cancel")
	}
}
