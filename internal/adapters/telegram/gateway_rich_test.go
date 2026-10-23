package telegram_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// richMarkdown decodes the markdown field of a rich_message form value.
func richMarkdown(t *testing.T, form url.Values) string {
	t.Helper()
	var rm struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(form.Get("rich_message")), &rm); err != nil {
		t.Fatalf("rich_message %q: %v", form.Get("rich_message"), err)
	}
	return rm.Markdown
}

func TestSendRichMarkdown(t *testing.T) {
	h := newHarness(t)
	h.gw.SetRichMarkdown(true)
	h.api.on("sendRichMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 9}) })
	md := "## Result\n\n| a | b |\n|---|---|\n| $1 | <T> |\n\nrun `echo $HOME`"
	id, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: md, Markdown: true, ReplyTo: 77, Footer: "⏱ 2 min",
		Buttons: []domain.Button{{Text: "ok", Data: "1"}}})
	if err != nil || id != 9 {
		t.Fatalf("Send = %d, %v", id, err)
	}
	if n := len(h.api.callsOf("sendMessage")); n != 0 {
		t.Fatalf("sendMessage calls = %d, want none", n)
	}
	calls := h.api.callsOf("sendRichMessage")
	if len(calls) != 1 {
		t.Fatalf("sendRichMessage calls = %d", len(calls))
	}
	f := calls[0].form
	want := "## Result\n\n| a | b |\n|---|---|\n| \\$1 | &lt;T> |\n\nrun `echo $HOME`\n\n⏱ 2 min"
	if got := richMarkdown(t, f); got != want {
		t.Errorf("markdown = %q, want %q", got, want)
	}
	if f.Get("message_thread_id") != "42" || f.Get("disable_notification") != "true" ||
		!strings.Contains(f.Get("reply_parameters"), `"message_id":77`) || !strings.Contains(f.Get("reply_markup"), `"callback_data":"1"`) {
		t.Errorf("form = %v", f)
	}
}

func TestSendRichOffAndNonMarkdownUseSendMessage(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })
	// Off by default: Markdown is rendered to HTML.
	if _, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: "**a**", Markdown: true}); err != nil {
		t.Fatal(err)
	}
	// On: code and plain posts still go through sendMessage.
	h.gw.SetRichMarkdown(true)
	if _, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: "screen", Code: true}); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.callsOf("sendMessage")); n != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", n)
	}
	if got := h.api.callsOf("sendMessage")[0].form.Get("text"); got != "<b>a</b>" {
		t.Errorf("html = %q", got)
	}
	if n := len(h.api.callsOf("sendRichMessage")); n != 0 {
		t.Fatalf("sendRichMessage calls = %d", n)
	}
}

func TestSendRichRejectedFallsBackToHTML(t *testing.T) {
	h := newHarness(t)
	h.gw.SetRichMarkdown(true)
	h.api.on("sendRichMessage", func(url.Values) apiReply { return errReply(400, "Bad Request: too many blocks") })
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 5}) })
	// Too long for one HTML message: the fallback re-splits the part.
	md := "**head**\n" + strings.Repeat("x", 5000)
	id, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: md, Markdown: true, ReplyTo: 77, Footer: "⏱ 2 min",
		Buttons: []domain.Button{{Text: "ok", Data: "1"}}})
	if err != nil || id != 5 {
		t.Fatalf("Send = %d, %v", id, err)
	}
	calls := h.api.callsOf("sendMessage")
	if len(calls) < 2 {
		t.Fatalf("sendMessage calls = %d, want the part re-split", len(calls))
	}
	first, last := calls[0].form, calls[len(calls)-1].form
	if !strings.HasPrefix(first.Get("text"), "<b>head</b>") || first.Get("parse_mode") != "HTML" || !strings.Contains(first.Get("reply_parameters"), `"message_id":77`) {
		t.Errorf("first fallback part = %v", first)
	}
	if !strings.HasSuffix(last.Get("text"), "⏱ 2 min") || !last.Has("reply_markup") || last.Has("reply_parameters") || first.Has("reply_markup") {
		t.Errorf("last fallback part = %v", last)
	}
}

func TestSendRichOtherErrorsAreReturned(t *testing.T) {
	h := newHarness(t)
	h.gw.SetRichMarkdown(true)
	h.api.on("sendRichMessage", func(url.Values) apiReply { return errReply(403, "Forbidden: bot is not a member") })
	if _, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: "a", Markdown: true}); err == nil {
		t.Fatal("Send succeeded after a 403")
	}
	if n := len(h.api.callsOf("sendMessage")); n != 0 {
		t.Fatalf("a 403 fell back to sendMessage: %d", n)
	}
}

func TestSendRichFoldedPartUsesHTML(t *testing.T) {
	h := newHarness(t)
	h.gw.SetRichMarkdown(true)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })
	text := strings.TrimSuffix(strings.Repeat("line\n", 25), "\n")
	if _, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: text, Markdown: true, Fold: 20}); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.callsOf("sendRichMessage")); n != 0 {
		t.Fatalf("a folded part went out rich: %d", n)
	}
	if got := h.api.callsOf("sendMessage")[0].form.Get("text"); !strings.HasPrefix(got, "<blockquote expandable>") {
		t.Errorf("folded = %q", got)
	}
}

func TestEditMarkdownRich(t *testing.T) {
	h := newHarness(t)
	h.gw.SetRichMarkdown(true)
	n := 0
	h.api.on("editMessageText", func(url.Values) apiReply {
		n++
		if n == 2 {
			return errReply(400, "Bad Request: rich message is invalid")
		}
		return okReply(map[string]any{"message_id": 3})
	})
	if err := h.gw.EditMarkdown(h.ctx, 3, "| a |\n|---|\n| $1 |"); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("editMessageText")
	if got := richMarkdown(t, calls[0].form); got != "| a |\n|---|\n| \\$1 |" || calls[0].form.Get("parse_mode") != "" {
		t.Errorf("rich edit = %q, form %v", got, calls[0].form)
	}
	// A rejected rich edit is made again as HTML.
	if err := h.gw.EditMarkdown(h.ctx, 3, "**b**"); err != nil {
		t.Fatal(err)
	}
	calls = h.api.callsOf("editMessageText")
	if len(calls) != 3 || calls[2].form.Get("text") != "<b>b</b>" || calls[2].form.Get("parse_mode") != "HTML" || calls[2].form.Has("rich_message") {
		t.Fatalf("fallback edit = %+v", calls)
	}
}
