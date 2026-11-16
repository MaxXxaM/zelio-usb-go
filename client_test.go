package zelio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeWire struct {
	replies [][]byte
	pending []byte
	writes  [][]byte
	chunk   int
	short   bool
	closed  bool
}

func (f *fakeWire) Write(_ context.Context, b []byte) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), b...))
	if f.short {
		return 1, nil
	}
	if len(f.replies) > 0 {
		f.pending = f.replies[0]
		f.replies = f.replies[1:]
	}
	return len(b), nil
}
func (f *fakeWire) Read(ctx context.Context, b []byte) (int, error) {
	if len(f.pending) == 0 {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	n := len(f.pending)
	if f.chunk > 0 && n > f.chunk {
		n = f.chunk
	}
	if n > len(b) {
		n = len(b)
	}
	copy(b, f.pending[:n])
	f.pending = f.pending[n:]
	return n, nil
}
func (f *fakeWire) Close() error { f.closed = true; return nil }
func newTestClient(t *testing.T, w *fakeWire, write bool) *Client {
	t.Helper()
	c, err := NewClient(w, Options{AllowMonitoringWrites: write, Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientSplitRead(t *testing.T) {
	w := &fakeWire{replies: [][]byte{[]byte(":0103020001FA\r\n")}, chunk: 1}
	c := newTestClient(t, w, false)
	words, err := c.ReadWords(context.Background(), 25, 1)
	if err != nil || len(words) != 1 || words[0] != 1 {
		t.Fatalf("%v %v", words, err)
	}
}

func TestInvalidAddressAndCancelledReadSendNothing(t *testing.T) {
	w := &fakeWire{}
	c := newTestClient(t, w, false)
	if _, err := c.ReadWords(context.Background(), 49, 1); err == nil {
		t.Fatal("range accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadWords(ctx, 25, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(w.writes) != 0 {
		t.Fatal("unexpected I/O")
	}
}

func TestConcurrentRequestsAreSerialized(t *testing.T) {
	w := &fakeWire{chunk: 1}
	for i := 0; i < 20; i++ {
		w.replies = append(w.replies, []byte(":0103020001FA\r\n"))
	}
	c := newTestClient(t, w, false)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.ReadWords(context.Background(), 25, 1)
			if err != nil || len(v) != 1 || v[0] != 1 {
				t.Errorf("%v %v", v, err)
			}
		}()
	}
	wg.Wait()
	if len(w.writes) != 20 {
		t.Fatal(len(w.writes))
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w := &fakeWire{}
	c := newTestClient(t, w, false)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !w.closed {
		t.Fatal("not closed")
	}
	if _, err := c.ReadWords(context.Background(), 25, 1); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
