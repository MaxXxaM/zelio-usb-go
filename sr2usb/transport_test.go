package sr2usb

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type fakeInput struct {
	packets [][]byte
	calls   int
}

func (f *fakeInput) ReadContext(ctx context.Context, b []byte) (int, error) {
	f.calls++
	if len(f.packets) == 0 {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	p := f.packets[0]
	f.packets = f.packets[1:]
	return copy(b, p), nil
}

type fakeOutput struct{ data []byte }

func (f *fakeOutput) WriteContext(_ context.Context, b []byte) (int, error) {
	f.data = append(f.data, b...)
	return len(b), nil
}

func TestTransportStripsStatusAndKeepsUnreadBytes(t *testing.T) {
	input := &fakeInput{packets: [][]byte{{1, 0x60}, {1, 0x60, 'a', 'b', 'c'}}}
	tr := &Transport{in: input}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b := make([]byte, 2)
	n, err := tr.Read(ctx, b)
	if err != nil || n != 2 || string(b) != "ab" {
		t.Fatalf("%q %d %v", b, n, err)
	}
	n, err = tr.Read(ctx, b)
	if err != nil || n != 1 || b[0] != 'c' || input.calls != 2 {
		t.Fatalf("%q %d calls=%d %v", b, n, input.calls, err)
	}
}

func TestTransportReadTimeoutAndLineError(t *testing.T) {
	tr := &Transport{in: &fakeInput{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := tr.Read(ctx, make([]byte, 8)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	tr = &Transport{in: &fakeInput{packets: [][]byte{{1, 0x64, ':', '0', '1'}}}}
	if n, err := tr.Read(context.Background(), make([]byte, 8)); err == nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestTransportWritesRawUARTAndRejectsClosed(t *testing.T) {
	output := &fakeOutput{}
	tr := &Transport{out: output}
	data := []byte(":01030000FF1802E4\r\n")
	if n, err := tr.Write(context.Background(), data); err != nil || n != len(data) || string(output.data) != string(data) {
		t.Fatalf("%q %d %v", output.data, n, err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Write(context.Background(), data); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := tr.Read(context.Background(), make([]byte, 8)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
