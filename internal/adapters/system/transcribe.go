package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// TranscriberName is the file name of the transcribe program in the
	// plugin config directory.
	TranscriberName = "transcribe"
	// transcribeTimeout bounds one transcription, model load included.
	transcribeTimeout = 3 * time.Minute
	// transcribeMaxOutput is the most stdout a transcription may produce.
	transcribeMaxOutput = 1 << 20
)

// CommandTranscriber implements domain.Transcriber by running a program
// the user puts in the plugin config directory: it gets the audio file's
// path as its only argument and prints the transcript on stdout. The
// program is looked up on every call, so adding or removing it needs no
// restart.
type CommandTranscriber struct {
	path    string
	timeout time.Duration
	log     *slog.Logger
}

var _ domain.Transcriber = (*CommandTranscriber)(nil)

// NewCommandTranscriber returns a transcriber running
// <configDir>/transcribe.
func NewCommandTranscriber(configDir string, log *slog.Logger) *CommandTranscriber {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CommandTranscriber{path: filepath.Join(configDir, TranscriberName), timeout: transcribeTimeout, log: log}
}

// Transcribe implements domain.Transcriber.
func (t *CommandTranscriber) Transcribe(ctx context.Context, path string) (string, error) {
	info, err := os.Stat(t.path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", domain.ErrTranscriberMissing
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.path, path)
	cmd.WaitDelay = time.Second
	stdout := &limitedWriter{max: transcribeMaxOutput}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	t.log.Debug("transcribe run", slog.String("program", t.path), slog.String("file", path),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()), slog.Int("bytes", stdout.buf.Len()), slog.Any("err", runErr))
	switch {
	case runErr == nil:
		return strings.TrimSpace(stdout.buf.String()), nil
	case ctx.Err() != nil:
		t.log.Warn("transcribe timed out", slog.String("program", t.path), slog.Duration("timeout", t.timeout))
		return "", context.DeadlineExceeded
	}
	// Libraries warn on stderr before the program's own reason, so the
	// last line is the one that explains the failure.
	reason := lastLine(stderr.String())
	t.log.Warn("transcribe failed", slog.String("program", t.path), slog.String("err", runErr.Error()), slog.String("stderr", reason))
	if reason == "" {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return "", fmt.Errorf("transcriber exited with status %d", exit.ExitCode())
		}
		return "", fmt.Errorf("transcriber: %w", runErr)
	}
	return "", errors.New(reason)
}

// lastLine returns the last non-blank line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
