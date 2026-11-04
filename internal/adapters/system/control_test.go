package system

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// controlFixture runs a control listener over a temporary state directory
// until the test ends.
type controlFixture struct {
	dir     string
	mu      sync.Mutex
	stops   int
	resyncs int
	status  string
	images  []string
	cancel  context.CancelFunc
	done    chan struct{}
}

func startControl(t *testing.T) *controlFixture {
	t.Helper()
	f := &controlFixture{dir: testkit.ShortTempDir(t), status: "version=test pid=1"}
	ln, err := ListenControl(f.dir, nil)
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		ServeControl(ctx, ln, ControlHandlers{
			Stop: func() {
				f.mu.Lock()
				f.stops++
				f.mu.Unlock()
			},
			Resync: func() {
				f.mu.Lock()
				f.resyncs++
				f.mu.Unlock()
			},
			Status: func() string {
				f.mu.Lock()
				defer f.mu.Unlock()
				return f.status
			},
			Image: func(_ context.Context, pane, path, caption string) (string, error) {
				if strings.HasSuffix(path, "missing.png") {
					return "", errors.New("image " + path + ": no such file")
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				f.images = append(f.images, pane+"|"+path+"|"+caption)
				return "sent " + path, nil
			},
		}, nil)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Error("ServeControl did not stop")
		}
	})
	return f
}

func (f *controlFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops, f.resyncs
}

func TestControlCommands(t *testing.T) {
	f := startControl(t)
	ctx := context.Background()

	if reply, err := SendControl(ctx, f.dir, ControlStop); err != nil || reply != "" {
		t.Fatalf("stop: reply = %q, err = %v", reply, err)
	}
	if reply, err := SendControl(ctx, f.dir, ControlResync); err != nil || reply != "" {
		t.Fatalf("resync: reply = %q, err = %v", reply, err)
	}
	if reply, err := SendControl(ctx, f.dir, ControlStatus); err != nil || reply != "version=test pid=1" {
		t.Fatalf("status: reply = %q, err = %v", reply, err)
	}
	if stops, resyncs := f.counts(); stops != 1 || resyncs != 1 {
		t.Fatalf("handlers called stop=%d resync=%d", stops, resyncs)
	}
}

func TestControlImage(t *testing.T) {
	f := startControl(t)
	ctx := context.Background()
	reply, err := SendImage(ctx, f.dir, "agentArt|wA:p2", "/Users/Me/Shots/A.png", "after\tthe\nfix ")
	if err != nil || reply != "sent /Users/Me/Shots/A.png" {
		t.Fatalf("SendImage = %q, %v", reply, err)
	}
	if _, err := SendImage(ctx, f.dir, "w1:p1", "/tmp/missing.png", ""); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing file err = %v", err)
	}
	if _, err := SendImage(ctx, f.dir, "w1:p1", "/tmp/a\tb.png", ""); err == nil {
		t.Fatal("a tab in the path was sent")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.images) != 1 || f.images[0] != "agentArt|wA:p2|/Users/Me/Shots/A.png|after the fix" {
		t.Fatalf("images = %q", f.images)
	}
}

func TestControlUnknownCommand(t *testing.T) {
	f := startControl(t)
	_, err := SendControl(context.Background(), f.dir, "explode")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want an unknown-command error", err)
	}
	if stops, resyncs := f.counts(); stops != 0 || resyncs != 0 {
		t.Fatalf("unknown command ran a handler: stop=%d resync=%d", stops, resyncs)
	}
}

func TestControlUnavailableWithoutListener(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	_, err := SendControl(context.Background(), dir, ControlStatus)
	if !errors.Is(err, domain.ErrControlUnavailable) {
		t.Fatalf("err = %v, want ErrControlUnavailable", err)
	}
}

func TestControlSecondListenerRefused(t *testing.T) {
	f := startControl(t)
	if _, err := ListenControl(f.dir, nil); err == nil {
		t.Fatal("a second listener was allowed while the first serves")
	}
}

func TestControlReplacesStaleSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes are released with the owning process")
	}
	dir := testkit.ShortTempDir(t)
	// A daemon that died without cleaning up leaves a socket file that
	// nothing answers.
	ln, err := net.Listen("unix", ControlPath(dir))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = ln.Close()
	if err := os.WriteFile(ControlPath(dir), nil, 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	ln2, err := ListenControl(dir, nil)
	if err != nil {
		t.Fatalf("ListenControl over a stale socket: %v", err)
	}
	_ = ln2.Close()
}

func TestControlListenerClosesOnCancel(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	ln, err := ListenControl(dir, nil)
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ServeControl(ctx, ln, ControlHandlers{}, nil)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ServeControl did not stop within 500ms")
	}
	if _, err := SendControl(context.Background(), dir, ControlStatus); !errors.Is(err, domain.ErrControlUnavailable) {
		t.Fatalf("after cancel: err = %v, want ErrControlUnavailable", err)
	}
}
