package transcript

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	progPrompt  = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"build it"}}`
	progText1   = `{"type":"assistant","isSidechain":false,"timestamp":"2026-09-28T21:38:01.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"  Starting on the modal.  "}]}}`
	progTool    = `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	progResult  = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	progSide    = `{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}`
	progText2   = `{"type":"assistant","isSidechain":false,"timestamp":"2026-09-28T21:38:38.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Committed PureModal."},{"type":"text","text":"Waiting on the app agent."}]}}`
	progPartial = `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"half wri`
)

func progressFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testReader(home string) *Reader {
	return newReader(func() (string, error) { return home, nil }, time.Now, slog.New(slog.DiscardHandler))
}

func texts(got []domain.ProgressText) []string {
	out := make([]string, 0, len(got))
	for _, g := range got {
		out = append(out, g.Text)
	}
	return out
}

func TestProgressSinceReadsMainTextForward(t *testing.T) {
	head := progPrompt + "\n"
	body := progText1 + "\n" + progTool + "\n" + progResult + "\n" + progSide + "\n" + progText2 + "\n"
	path := progressFile(t, head, body, progPartial)
	r := testReader(t.TempDir())

	got, next, err := r.ProgressSince(context.Background(), path, int64(len(head)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Starting on the modal.", "Committed PureModal.", "Waiting on the app agent."}
	if strings.Join(texts(got), "|") != strings.Join(want, "|") {
		t.Fatalf("texts = %q, want %q", texts(got), want)
	}
	if !got[0].Written.Equal(time.Date(2026, 9, 28, 21, 38, 1, 0, time.UTC)) {
		t.Errorf("written = %v", got[0].Written)
	}
	// The half-written record is left for the next read.
	if want := int64(len(head) + len(body)); next != want {
		t.Fatalf("next = %d, want %d", next, want)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`tten"}]}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, _, err = r.ProgressSince(context.Background(), path, next)
	if err != nil || len(got) != 1 || got[0].Text != "half written" {
		t.Fatalf("second read = %q, %v", texts(got), err)
	}
}

func TestProgressSinceNothingNewAndRewrittenFile(t *testing.T) {
	path := progressFile(t, progText1+"\n")
	r := testReader(t.TempDir())
	size := int64(len(progText1) + 1)
	if got, next, err := r.ProgressSince(context.Background(), path, size); err != nil || len(got) != 0 || next != size {
		t.Fatalf("at end = %q, %d, %v", texts(got), next, err)
	}
	// An offset past the end means the file was replaced: read it all.
	if got, _, err := r.ProgressSince(context.Background(), path, size+500); err != nil || len(got) != 1 {
		t.Fatalf("rewritten = %q, %v", texts(got), err)
	}
	if _, _, err := r.ProgressSince(context.Background(), filepath.Join(t.TempDir(), "gone.jsonl"), 0); !errors.Is(err, domain.ErrNoReply) {
		t.Fatalf("missing file err = %v", err)
	}
}

func TestProgressFromBudgetSkipsCutLine(t *testing.T) {
	data := progText1 + "\n" + progText2 + "\n"
	budget := int64(len(progText2) + 10) // starts inside the first record
	got, next, _, err := progressFrom(strings.NewReader(data), int64(len(data)), 0, budget)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts(got), "|") != "Committed PureModal.|Waiting on the app agent." || next != int64(len(data)) {
		t.Fatalf("texts = %q, next %d", texts(got), next)
	}
}

func TestProgressTranscriptNeedsExactSession(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/op/Projects/demo"
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.jsonl"), []byte(progText1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A newer session of another pane in the same directory.
	if err := os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte(progText2+"\n"+progText2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := testReader(home)
	agent := domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "mine"}
	path, size, err := r.ProgressTranscript(context.Background(), agent)
	if err != nil || filepath.Base(path) != "mine.jsonl" || size != int64(len(progText1)+1) {
		t.Fatalf("ProgressTranscript = %s, %d, %v", path, size, err)
	}
	// The session started in another directory: found by its id anywhere.
	agent.Cwd = "/Users/op"
	if path, _, err := r.ProgressTranscript(context.Background(), agent); err != nil || filepath.Base(path) != "mine.jsonl" {
		t.Fatalf("moved cwd = %s, %v", path, err)
	}
	for reason, a := range map[string]domain.Agent{
		"no session":                     {Kind: "claude", Cwd: cwd},
		"no transcript":                  {Kind: "claude", Cwd: cwd, SessionID: "missing"},
		"unsupported":                    {Kind: "codex", Cwd: cwd, SessionID: "mine"},
		"no transcript for session ../x": {Kind: "claude", Cwd: cwd, SessionID: "../x"},
	} {
		if _, _, err := r.ProgressTranscript(context.Background(), a); !errors.Is(err, domain.ErrNoReply) {
			t.Errorf("%s: err = %v", reason, err)
		}
	}
}

func TestLastReplyPrefersExactSession(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/op/Projects/demo"
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "mine.jsonl")
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(mine, []byte(progPrompt+"\n"+progText1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(progPrompt+"\n"+progText2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(mine, older, older); err != nil {
		t.Fatal(err)
	}
	r := testReader(home)
	reply, err := r.LastReply(context.Background(), domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "mine"})
	if err != nil || reply.Source != mine || reply.Text != "Starting on the modal." {
		t.Fatalf("exact session reply = %+v, %v", reply, err)
	}
	// Without a session the newest file of the directory still answers.
	reply, err = r.LastReply(context.Background(), domain.Agent{Kind: "claude", Cwd: cwd})
	if err != nil || reply.Source != other {
		t.Fatalf("newest file reply = %+v, %v", reply, err)
	}
}

// pngBytes is a w×h PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// imageResult is a user record whose tool result for id holds one base64
// image.
func imageResult(id, mediaType string, data []byte, sidechain bool) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":%t,"timestamp":"2026-10-02T15:04:05.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":[{"type":"image","source":{"type":"base64","media_type":%q,"data":%q}}]}]}}`,
		sidechain, id, mediaType, base64.StdEncoding.EncodeToString(data))
}

func TestProgressSinceReadsToolImagesInOrder(t *testing.T) {
	shot := pngBytes(t, 4, 3)
	read := `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/repo/test-results/screenshots/landing-desktop.png"}}]}}`
	mcp := `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"m1","name":"mcp__playwright__browser_take_screenshot","input":{}}]}}`
	attached := `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(shot) + `"}},{"type":"text","text":"look"}]}}`
	broken := `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%%%"}}]}]}}`
	path := progressFile(t,
		progText1+"\n",
		read+"\n", imageResult("r1", "image/png", shot, false)+"\n",
		attached+"\n",
		imageResult("r1", "image/png", shot, true)+"\n",
		broken+"\n",
		mcp+"\n", imageResult("m1", "image/jpeg", []byte("jpeg bytes"), false)+"\n",
		imageResult("earlier", "image/png", shot, false)+"\n",
		progText2+"\n")
	got, _, err := testReader(t.TempDir()).ProgressSince(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, g := range got {
		if g.Image != nil {
			seq = append(seq, "img:"+g.Image.Name+":"+g.Image.MediaType)
		} else {
			seq = append(seq, g.Text)
		}
	}
	want := []string{"Starting on the modal.", "img:landing-desktop.png:image/png", "img:browser_take_screenshot.jpg:image/jpeg",
		"img:image.png:image/png", "Committed PureModal.", "Waiting on the app agent."}
	if strings.Join(seq, "|") != strings.Join(want, "|") {
		t.Fatalf("items = %q, want %q", seq, want)
	}
	if !bytes.Equal(got[1].Image.Data, shot) || got[1].Text != "" {
		t.Errorf("image data = %d bytes, text %q", len(got[1].Image.Data), got[1].Text)
	}
	if !got[1].Written.Equal(time.Date(2026, 10, 2, 15, 4, 5, 0, time.UTC)) {
		t.Errorf("written = %v", got[1].Written)
	}
}
