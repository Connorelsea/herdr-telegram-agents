package telegram_test

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/url"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSendPhotoSendsASilentPhoto(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendPhoto", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 5}) })
	data := testPNG(t, 1285, 1344)
	if err := h.gw.SendPhoto(h.ctx, domain.Document{ThreadID: 42, Name: "landing-desktop.png", Data: data, Caption: "🖼 landing-desktop.png · 15:04"}); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendPhoto")
	if len(calls) != 1 || len(h.api.callsOf("sendDocument")) != 0 {
		t.Fatalf("sendPhoto calls = %d, sendDocument = %d", len(calls), len(h.api.callsOf("sendDocument")))
	}
	c := calls[0]
	if c.form.Get("message_thread_id") != "42" || c.form.Get("disable_notification") != "true" || c.form.Get("caption") != "🖼 landing-desktop.png · 15:04" {
		t.Errorf("form = %v", c.form)
	}
	if file, ok := c.files["photo"]; !ok || file.name != "landing-desktop.png" || !bytes.Equal(file.data, data) {
		t.Fatalf("photo part = %+v (ok %v)", file, ok)
	}
}

func TestSendPhotoSendsUnsuitableImagesAsFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"full-page screenshot", testPNG(t, 800, 6000)},
		{"too long a ratio", testPNG(t, 2100, 100)},
		{"undecodable", []byte("RIFF....WEBP")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.on("sendDocument", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 6}) })
			if err := h.gw.SendPhoto(h.ctx, domain.Document{ThreadID: 42, Name: "page.png", Data: tc.data, Caption: "cap"}); err != nil {
				t.Fatal(err)
			}
			if n := len(h.api.callsOf("sendPhoto")); n != 0 {
				t.Fatalf("sendPhoto calls = %d, want 0", n)
			}
			calls := h.api.callsOf("sendDocument")
			if len(calls) != 1 || calls[0].form.Get("caption") != "cap" || calls[0].form.Get("disable_content_type_detection") == "true" {
				t.Fatalf("sendDocument calls = %+v", calls)
			}
			if file := calls[0].files["document"]; !bytes.Equal(file.data, tc.data) {
				t.Fatalf("document bytes changed")
			}
		})
	}
}

func TestSendPhotoFallsBackToAFileWhenThePhotoIsRejected(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendPhoto", func(url.Values) apiReply { return errReply(400, "Bad Request: IMAGE_PROCESS_FAILED") })
	h.api.on("sendDocument", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 6}) })
	if err := h.gw.SendPhoto(h.ctx, domain.Document{ThreadID: 42, Name: "a.png", Data: testPNG(t, 10, 10)}); err != nil {
		t.Fatal(err)
	}
	if len(h.api.callsOf("sendPhoto")) != 1 || len(h.api.callsOf("sendDocument")) != 1 {
		t.Fatalf("calls: photo %d, document %d", len(h.api.callsOf("sendPhoto")), len(h.api.callsOf("sendDocument")))
	}
}

func TestSendPhotoToAGoneTopicFails(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendPhoto", func(url.Values) apiReply { return errReply(400, "Bad Request: message thread not found") })
	err := h.gw.SendPhoto(h.ctx, domain.Document{ThreadID: 9, Name: "a.png", Data: testPNG(t, 10, 10)})
	if err == nil || !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("err = %v, want ErrTopicGone", err)
	}
	if n := len(h.api.callsOf("sendDocument")); n != 0 {
		t.Fatalf("a gone topic must not be retried as a file, sendDocument = %d", n)
	}
}
