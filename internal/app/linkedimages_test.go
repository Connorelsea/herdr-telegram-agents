package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// fakeImageFiles serves the image files of its map; any other path is
// missing. Bump changes a file's modification time.
type fakeImageFiles struct {
	files map[string]time.Time
}

func newFakeImageFiles(paths ...string) *fakeImageFiles {
	f := &fakeImageFiles{files: map[string]time.Time{}}
	for _, p := range paths {
		f.files[p] = tb0
	}
	return f
}

func (f *fakeImageFiles) bump(path string) { f.files[path] = f.files[path].Add(time.Second) }

func (f *fakeImageFiles) ReadImage(_ context.Context, path string) (domain.ImageFile, error) {
	mod, ok := f.files[path]
	if !ok {
		return domain.ImageFile{}, fmt.Errorf("image %s: no such file", path)
	}
	name := filepath.Base(path)
	return domain.ImageFile{Path: path, Modified: mod,
		Image: domain.ProgressImage{Name: name, MediaType: "image/png", Data: []byte("png:" + name)}}, nil
}

// withLinked gives the fixture's Progress the named image files.
func (f *progressFixture) withLinked(on bool, paths ...string) *fakeImageFiles {
	files := newFakeImageFiles(paths...)
	f.progress.linked = newLinkedImages(files, func() bool { return on }, nil)
	return files
}

func TestProgressPostsNamedImageBelowItsText(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.withLinked(true, "/tmp/shot.png")
	f.step(0)
	f.write("Took a screenshot: /tmp/shot.png and /tmp/missing.png")
	f.write("Now the footer.")
	f.step(10 * time.Second)
	if got := strings.Join(kinds(f.calls()), " "); got != "send close photo:shot.png send" {
		t.Fatalf("calls = %q", got)
	}
	if photo := f.tg.Photos()[0]; photo.ThreadID != f.threadID || string(photo.Data) != "png:shot.png" {
		t.Fatalf("photo = %+v", photo)
	}
}

func TestProgressPostsEachVersionOfANamedImageOnce(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsProgress, "messages", domain.OptionPostsDone, "formatted")
	files := f.withLinked(true, "/tmp/shot.png")
	f.step(0)
	f.write("See /tmp/shot.png")
	f.step(10 * time.Second)
	f.write("Still /tmp/shot.png")
	f.step(10 * time.Second)
	files.bump("/tmp/shot.png")
	f.write("Retook /tmp/shot.png")
	f.step(10 * time.Second)
	if got := strings.Join(kinds(f.calls()), " "); got != "send photo:shot.png send send photo:shot.png" {
		t.Fatalf("calls = %q", got)
	}
}

func TestProgressLeavesNamedImagesAloneWithTheOptionOff(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsProgress, "messages", domain.OptionPostsDone, "formatted")
	f.withLinked(false, "/tmp/shot.png")
	f.step(0)
	f.write("See /tmp/shot.png")
	f.step(10 * time.Second)
	if got := strings.Join(kinds(f.calls()), " "); got != "send" {
		t.Fatalf("calls = %q", got)
	}
}

func TestProgressLeavesTheReplysNamedImagesToTheDonePost(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.withLinked(true, "/tmp/final.png")
	f.step(0)
	f.write("Done, the page is at /tmp/final.png") // the reply the done post carries
	f.agent.Status = domain.StatusIdle
	f.step(time.Second)
	f.step(6 * time.Second)
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("calls = %q", got)
	}
}

func TestOutboundDonePostsNamedImagesBelowTheReply(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.opts.Set(f.ctx, domain.OptionPostsDone, string(domain.DoneFormatted), 1); err != nil {
		t.Fatal(err)
	}
	f.out.linked = newLinkedImages(newFakeImageFiles("/tmp/final.png"), f.opts.PostsImagePaths, nil)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.replies.Set(a.Key, "Done, the page is at `/tmp/final.png`.")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	if got := strings.Join(kinds(f.tg.Calls()), " "); got != "send photo:final.png" {
		t.Fatalf("calls = %q", got)
	}
	if photo := f.tg.Photos()[0]; photo.Caption != "🖼 final.png" || string(photo.Data) != "png:final.png" {
		t.Fatalf("photo = %+v", photo)
	}
}

func TestOutboundScreenDonePostSkipsNamedImages(t *testing.T) {
	f := newBridgeFixture(t)
	f.out.linked = newLinkedImages(newFakeImageFiles("/tmp/final.png"), f.opts.PostsImagePaths, nil)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "Read(/tmp/final.png)")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	if photos := f.tg.Photos(); len(photos) != 0 {
		t.Fatalf("photos = %+v", photos)
	}
}

func TestBridgeSendImage(t *testing.T) {
	f := newBridgeFixture(t)
	files := newFakeImageFiles("/tmp/shot.png")
	f.out.linked = newLinkedImages(files, f.opts.PostsImagePaths, nil)
	b := &Bridge{out: f.out, images: files, log: f.out.log}
	a := f.add(t, "s|w1:p1", "t1", "reviewer", domain.StatusWorking)

	sent, err := b.SendImage(f.ctx, "s|w1:p1", "/tmp/shot.png", "")
	if err != nil || sent != "sent shot.png to "+a.Label() {
		t.Fatalf("SendImage = %q, %v", sent, err)
	}
	if photos := f.tg.Photos(); len(photos) != 1 || photos[0].Caption != "🖼 shot.png" {
		t.Fatalf("photos = %+v", photos)
	}
	if _, err := b.SendImage(f.ctx, "s|w1:p1", "/tmp/shot.png", "after the fix"); err != nil {
		t.Fatalf("SendImage with caption: %v", err)
	}
	if photos := f.tg.Photos(); len(photos) != 2 || photos[1].Caption != "after the fix" {
		t.Fatalf("photos = %+v", photos)
	}
	// What was sent is not posted again when a reply names it.
	if got := f.out.linked.find(f.ctx, a.Key, "see /tmp/shot.png"); len(got) != 0 {
		t.Fatalf("named again = %+v", got)
	}

	for pane, want := range map[string]error{"w9:p9": errSendNoAgent} {
		if _, err := b.SendImage(f.ctx, pane, "/tmp/shot.png", ""); !errors.Is(err, want) {
			t.Errorf("SendImage(%s) err = %v, want %v", pane, err, want)
		}
	}
	if _, err := b.SendImage(f.ctx, "s|w1:p1", "/tmp/missing.png", ""); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing file err = %v", err)
	}
	if _, err := (&Bridge{out: f.out}).SendImage(f.ctx, "s|w1:p1", "/tmp/shot.png", ""); !errors.Is(err, errSendNoImages) {
		t.Errorf("no image reader err = %v", err)
	}
}
