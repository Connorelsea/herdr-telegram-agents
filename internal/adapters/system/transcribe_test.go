//go:build !windows

package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// writeProgram puts an executable shell script named transcribe in a new
// directory and returns the directory.
func writeProgram(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, TranscriberName), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCommandTranscriberPrintsTranscript(t *testing.T) {
	dir := writeProgram(t, `printf '  hello from %s  \n' "$(basename "$1")"`)
	got, err := NewCommandTranscriber(dir, nil).Transcribe(context.Background(), "/inbox/voice.ogg")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello from voice.ogg" {
		t.Fatalf("Transcribe = %q", got)
	}
}

func TestCommandTranscriberMissingProgram(t *testing.T) {
	_, err := NewCommandTranscriber(t.TempDir(), nil).Transcribe(context.Background(), "/inbox/voice.ogg")
	if !errors.Is(err, domain.ErrTranscriberMissing) {
		t.Fatalf("err = %v, want ErrTranscriberMissing", err)
	}
	// A file without the executable bit is not a transcriber either.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, TranscriberName), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCommandTranscriber(dir, nil).Transcribe(context.Background(), "x"); !errors.Is(err, domain.ErrTranscriberMissing) {
		t.Fatalf("err = %v, want ErrTranscriberMissing", err)
	}
}

func TestCommandTranscriberFailureCarriesLastStderrLine(t *testing.T) {
	dir := writeProgram(t, "echo 'library warning' >&2\necho 'model directory not found: /m' >&2\nexit 1")
	_, err := NewCommandTranscriber(dir, nil).Transcribe(context.Background(), "x")
	if err == nil || err.Error() != "model directory not found: /m" {
		t.Fatalf("err = %v", err)
	}
	dir = writeProgram(t, "exit 3")
	_, err = NewCommandTranscriber(dir, nil).Transcribe(context.Background(), "x")
	if err == nil || err.Error() != "transcriber exited with status 3" {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandTranscriberTimeout(t *testing.T) {
	dir := writeProgram(t, "sleep 5")
	tr := NewCommandTranscriber(dir, nil)
	tr.timeout = 50 * time.Millisecond
	if _, err := tr.Transcribe(context.Background(), "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
