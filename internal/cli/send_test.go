package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSend(t *testing.T) {
	var gotPath, gotCaption string
	orig := wire.sendImage
	t.Cleanup(func() { wire.sendImage = orig })
	wire.sendImage = func(_ context.Context, path, caption string) (string, error) {
		if strings.HasSuffix(path, "missing.png") {
			return "", errors.New("image " + path + ": no such file")
		}
		gotPath, gotCaption = path, caption
		return "sent a.png to ws · claude", nil
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"send", "shots/a.png", "after", "the", "fix"}, "test", &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	abs, _ := filepath.Abs("shots/a.png")
	if gotPath != abs || gotCaption != "after the fix" || out.String() != "sent a.png to ws · claude\n" {
		t.Fatalf("path = %q caption = %q out = %q", gotPath, gotCaption, out.String())
	}
	if Run([]string{"send", "~/a.png"}, "test", &out, &errOut); gotPath != "~/a.png" {
		t.Fatalf("home path = %q", gotPath)
	}
	errOut.Reset()
	if code := Run([]string{"send", "/tmp/missing.png"}, "test", &out, &errOut); code != exitError || !strings.Contains(errOut.String(), "no such file") {
		t.Fatalf("missing: exit = %d, stderr = %s", code, errOut.String())
	}
	if code := Run([]string{"send"}, "test", &out, &errOut); code != exitUsage {
		t.Fatalf("no args: exit = %d", code)
	}
}
