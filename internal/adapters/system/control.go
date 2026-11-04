package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// ControlFileName is the daemon's control socket inside the state
	// directory on Unix. Windows uses a named pipe instead; see
	// ControlPath.
	ControlFileName = "control.sock"
	// controlTimeout bounds one control exchange on both sides;
	// controlImageTimeout bounds an image send, which uploads the file.
	controlTimeout      = 2 * time.Second
	controlImageTimeout = 60 * time.Second
	// controlMaxLine caps a command line so a stray writer cannot make the
	// daemon buffer without end; it leaves room for a path and a caption.
	controlMaxLine = 4096
)

// Control commands understood by the daemon.
const (
	ControlStop   = "stop"
	ControlResync = "resync"
	ControlStatus = "status"
	// ControlImage posts an image into an agent's topic; its arguments
	// follow on the same line, separated by tabs: pane, path, caption.
	ControlImage = "image"
)

// ControlHandlers are the daemon actions the channel exposes. Stop and
// Resync must not block; Status returns one line. Image posts the file at
// path into the topic of the agent in pane and says what it sent.
type ControlHandlers struct {
	Stop   func()
	Resync func()
	Status func() string
	Image  func(ctx context.Context, pane, path, caption string) (string, error)
}

// ServeControl answers one command per connection until ctx is done, then
// closes the listener. It never returns an error: a broken connection is
// logged and the loop continues, because the daemon must survive whatever
// a client does.
func ServeControl(ctx context.Context, ln net.Listener, h ControlHandlers, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log.Info("control listening", slog.String("addr", ln.Addr().String()))
	defer log.Debug("control listener stopped")

	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = ln.Close()
	}()
	defer func() {
		close(done)
		wg.Wait()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn("control accept failed", slog.String("err", err.Error()))
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveControlConn(conn, h, log)
		}()
	}
}

// serveControlConn reads one command, answers it and closes the connection.
func serveControlConn(conn net.Conn, h ControlHandlers, log *slog.Logger) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))

	line, err := bufio.NewReader(io.LimitReader(conn, controlMaxLine)).ReadString('\n')
	// The command word is matched without case; its arguments (a path)
	// keep theirs.
	args := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	cmd := strings.ToLower(strings.TrimSpace(args[0]))
	args = args[1:]
	if err != nil && cmd == "" {
		log.Warn("control read failed", slog.String("err", err.Error()))
		return
	}
	log.Debug("control command", slog.String("cmd", cmd))

	reply := "error: unknown command " + cmd
	switch cmd {
	case ControlStop:
		if h.Stop != nil {
			h.Stop()
		}
		reply = "ok"
	case ControlResync:
		if h.Resync != nil {
			h.Resync()
		}
		reply = "ok"
	case ControlStatus:
		reply = "ok"
		if h.Status != nil {
			reply = "ok " + strings.ReplaceAll(h.Status(), "\n", " ")
		}
	case ControlImage:
		reply = controlImage(conn, h, args)
	}
	if _, err := io.WriteString(conn, reply+"\n"); err != nil {
		log.Warn("control reply failed", slog.String("cmd", cmd), slog.String("err", err.Error()))
		return
	}
	log.Debug("control answered", slog.String("cmd", cmd), slog.String("reply", reply))
}

// controlImage serves ControlImage: pane, path and an optional caption.
func controlImage(conn net.Conn, h ControlHandlers, args []string) string {
	if h.Image == nil {
		return "error: image sending is not available"
	}
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		return "error: image needs a pane and a path"
	}
	caption := ""
	if len(args) > 2 {
		caption = args[2]
	}
	_ = conn.SetDeadline(time.Now().Add(controlImageTimeout))
	ctx, cancel := context.WithTimeout(context.Background(), controlImageTimeout)
	defer cancel()
	sent, err := h.Image(ctx, args[0], args[1], caption)
	if err != nil {
		return "error: " + strings.ReplaceAll(err.Error(), "\n", " ")
	}
	return "ok " + sent
}

// SendImage asks the daemon listening for stateDir to post the image at
// path (absolute) into the topic of the agent in pane, and returns what
// it sent. Tabs and line breaks in the caption become spaces.
func SendImage(ctx context.Context, stateDir, pane, path, caption string) (string, error) {
	if strings.ContainsAny(pane+path, "\t\r\n") {
		return "", fmt.Errorf("control %s: tab or line break in the pane or path", ControlImage)
	}
	caption = strings.Join(strings.Fields(caption), " ")
	return sendControl(ctx, stateDir, ControlImage, strings.Join([]string{ControlImage, pane, path, caption}, "\t"), controlImageTimeout)
}

// SendControl sends one command to the daemon listening for stateDir and
// returns its reply without the "ok " prefix. A daemon that is not
// listening yields ErrControlUnavailable so callers can fall back.
func SendControl(ctx context.Context, stateDir, cmd string) (string, error) {
	return sendControl(ctx, stateDir, cmd, cmd, controlTimeout)
}

// sendControl writes line, the command cmd with its arguments, and reads
// the reply within timeout.
func sendControl(ctx context.Context, stateDir, cmd, line string, timeout time.Duration) (string, error) {
	conn, err := dialControl(ctx, ControlPath(stateDir))
	if err != nil {
		return "", fmt.Errorf("control %s: %w", cmd, domain.ErrControlUnavailable)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return "", fmt.Errorf("control %s: write: %w", cmd, err)
	}
	line, err = bufio.NewReader(io.LimitReader(conn, controlMaxLine)).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", fmt.Errorf("control %s: read: %w", cmd, err)
	}
	if rest, ok := strings.CutPrefix(line, "ok"); ok {
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("control %s: %s", cmd, line)
}
