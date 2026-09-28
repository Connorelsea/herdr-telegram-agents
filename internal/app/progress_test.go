package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// fakeProgressSource is a transcript as a list of texts; an offset is an
// index into it.
type fakeProgressSource struct {
	mu    sync.Mutex
	texts []domain.ProgressText
	err   error
	reads int
}

func (s *fakeProgressSource) add(text string, written time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, domain.ProgressText{Text: text, Written: written})
}

func (s *fakeProgressSource) ProgressTranscript(_ context.Context, a domain.Agent) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", 0, s.err
	}
	return "/t/" + a.SessionID + ".jsonl", int64(len(s.texts)), nil
}

func (s *fakeProgressSource) ProgressSince(_ context.Context, _ string, offset int64) ([]domain.ProgressText, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if offset > int64(len(s.texts)) {
		offset = 0
	}
	return append([]domain.ProgressText(nil), s.texts[offset:]...), int64(len(s.texts)), nil
}

type progressFixture struct {
	t        *testing.T
	tg       *testkit.FakeTelegram
	src      *fakeProgressSource
	clock    *testkit.FakeClock
	agent    domain.Agent
	threadID int
	progress *Progress
	ctx      context.Context
}

func newProgressFixture(t *testing.T, options ...string) *progressFixture {
	t.Helper()
	values := domain.DefaultOptions()
	for i := 0; i+1 < len(options); i += 2 {
		var err error
		if values, err = values.With(options[i], options[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	f := &progressFixture{
		t:     t,
		tg:    testkit.NewFakeTelegram(nil),
		src:   &fakeProgressSource{},
		clock: testkit.NewFakeClock(tb0),
		ctx:   context.Background(),
		agent: domain.Agent{Key: domain.Key{PaneID: "w5:p1", TerminalID: "t1"}, Kind: "claude", SessionID: "s1", Status: domain.StatusWorking},
	}
	topic, err := f.tg.CreateTopic(f.ctx, "Permissions UI", domain.StatusWorking)
	if err != nil {
		t.Fatal(err)
	}
	f.threadID = topic.ThreadID
	live := func() []domain.Agent { return []domain.Agent{f.agent} }
	topics := func(key domain.Key) (domain.TopicEntry, bool) {
		return domain.TopicEntry{ThreadID: f.threadID}, key == f.agent.Key
	}
	f.progress = NewProgress(f.tg, f.src, live, topics, NewOptions(values, nil, nil, nil), f.clock, nil)
	return f
}

// step advances the clock by d and runs one tick.
func (f *progressFixture) step(d time.Duration) {
	f.clock.Advance(d)
	f.progress.tick(f.ctx)
}

// write adds a transcript text written now.
func (f *progressFixture) write(text string) { f.src.add(text, f.clock.Now()) }

// calls are the fake's send and editmd calls, in order.
func (f *progressFixture) calls() []string {
	var out []string
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, "send:") || strings.HasPrefix(c, "editmd:") {
			out = append(out, c)
		}
	}
	return out
}

func stamp(t time.Time) string { return "_" + t.Local().Format("15:04") + "_" }

func TestProgressLiveMessageGrowsAndLeavesTheReplyToTheDonePost(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.step(0) // first sight: follows from the end of the transcript
	f.write("Starting on the modal.")
	f.step(5 * time.Second) // 5 s old: still held
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("young text posted: %q", got)
	}
	f.step(5 * time.Second)
	sent := f.tg.Sent()
	if len(sent) != 1 || !sent[0].Markdown || sent[0].Notify || sent[0].ThreadID != f.threadID ||
		sent[0].Text != "**⏳ Working**\n\n"+stamp(tb0)+"\nStarting on the modal." {
		t.Fatalf("first post = %+v", sent)
	}
	f.write("Committed PureModal.")
	f.step(10 * time.Second)
	f.write("All done.") // the reply that ends the turn
	f.agent.Status = domain.StatusIdle
	f.step(time.Second)
	f.step(5 * time.Second) // idle 6 s: the turn is over
	calls := f.calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %q", calls)
	}
	if !strings.HasPrefix(calls[1], "editmd:1000:**⏳ Working** · 2 updates") || !strings.Contains(calls[1], "Committed PureModal.") {
		t.Errorf("growing edit = %q", calls[1])
	}
	if !strings.HasPrefix(calls[2], "editmd:1000:**Progress** · 2 updates") || strings.Contains(calls[2], "All done.") {
		t.Errorf("final edit = %q", calls[2])
	}
	// The next turn starts a new live message.
	f.agent.Status = domain.StatusWorking
	f.step(time.Second)
	f.write("Second turn.")
	f.step(10 * time.Second)
	if sent := f.tg.Sent(); len(sent) != 2 || !strings.HasSuffix(sent[1].Text, "Second turn.") {
		t.Fatalf("second turn = %+v", sent)
	}
}

func TestProgressSkipsTextOutsideATurnAndTheBacklog(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.write("Written before the daemon saw the agent.")
	f.step(0)
	f.agent.Status = domain.StatusIdle
	f.write("Written while idle.")
	f.step(30 * time.Second)
	f.agent.Status = domain.StatusWorking
	f.step(30 * time.Second)
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("calls = %q", got)
	}
}

func TestProgressPostsTheLastTextWhenTheDonePostShowsTheScreen(t *testing.T) {
	f := newProgressFixture(t) // posts.done defaults to screen
	f.step(0)
	f.write("Only text.")
	f.agent.Status = domain.StatusIdle
	f.step(time.Second)
	f.step(6 * time.Second)
	sent := f.tg.Sent()
	if len(sent) != 1 || !strings.HasSuffix(sent[0].Text, "Only text.") {
		t.Fatalf("sent = %+v", sent)
	}
	if calls := f.calls(); len(calls) != 2 || !strings.HasPrefix(calls[1], "editmd:1000:**Progress**") {
		t.Fatalf("calls = %q", calls)
	}
}

func TestProgressMessagesModeSendsEachBatch(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsProgress, "messages", domain.OptionPostsDone, "formatted")
	f.step(0)
	f.write("One.")
	f.write("Two.")
	f.step(10 * time.Second)
	f.write("Three.")
	f.step(10 * time.Second)
	sent := f.tg.Sent()
	if len(sent) != 2 || sent[0].Text != stamp(tb0)+"\nOne.\n\n"+stamp(tb0)+"\nTwo." || !strings.HasSuffix(sent[1].Text, "Three.") {
		t.Fatalf("sent = %+v", sent)
	}
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "editmd:") {
			t.Fatalf("messages mode edited: %q", c)
		}
	}
}

func TestProgressLiveMessageRollsOverWhenFull(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.step(0)
	long := strings.Repeat("x", progressBudget/2)
	f.write("A" + long)
	f.write("B" + long)
	f.write("C" + long)
	f.step(10 * time.Second)
	sent := f.tg.Sent()
	if len(sent) != 3 {
		t.Fatalf("messages = %d, want one per entry: %+v", len(sent), f.calls())
	}
	for i, s := range sent {
		if n := len([]rune(s.Text)); n > progressBudget {
			t.Errorf("message %d is %d runes", i, n)
		}
	}
	if got := f.tg.Text(1000); !strings.HasPrefix(got, "**Progress**") {
		t.Errorf("full message not closed: %.40q", got)
	}
}

func TestProgressUnavailableTranscriptAndOptions(t *testing.T) {
	f := newProgressFixture(t)
	f.src.err = fmt.Errorf("%w: Herdr reports no session", domain.ErrNoReply)
	f.step(0)
	f.src.err = nil
	f.write("Text.")
	f.step(10 * time.Second) // still inside the retry wait
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("calls = %q", got)
	}

	off := newProgressFixture(t, domain.OptionPostsProgress, "off")
	off.step(0)
	off.write("Text.")
	off.step(10 * time.Second)
	if len(off.calls()) != 0 || off.src.reads != 0 {
		t.Fatalf("progress off: calls %q, reads %d", off.calls(), off.src.reads)
	}
}

func TestProgressForgetsADeletedLiveMessage(t *testing.T) {
	f := newProgressFixture(t, domain.OptionPostsDone, "formatted")
	f.step(0)
	f.write("One.")
	f.step(10 * time.Second)
	if err := f.tg.DeleteMessage(f.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	f.write("Two.")
	f.step(10 * time.Second)
	f.write("Three.")
	f.step(10 * time.Second)
	sent := f.tg.Sent()
	if len(sent) != 2 || !strings.HasSuffix(sent[1].Text, "Three.") {
		t.Fatalf("after delete = %+v", sent)
	}
}
