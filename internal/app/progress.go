package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// progressInterval is how often the transcripts of working agents are
	// read for new text.
	progressInterval = 5 * time.Second
	// progressSettle is how long an agent must stay out of working and
	// blocked before its turn counts as over. Herdr's detection dips out
	// of working for a second or two while a tool runs; the done post
	// waits 5 s of idle for the same reason.
	progressSettle = 6 * time.Second
	// progressHold keeps the newest text back this long before posting
	// it. The reply that ends a turn is written moments before the agent
	// stops, so it is still held when the turn ends and goes out only in
	// the done post instead of twice.
	progressHold = 8 * time.Second
	// progressBudget bounds the Markdown of one live message, below
	// Telegram's 4096-character cap with room for the rendered markup.
	progressBudget = 3500
	// progressRetry is how long an agent whose transcript cannot be
	// followed waits before the next attempt.
	progressRetry = time.Minute
)

// Progress posts the text a Claude Code agent writes to its transcript
// while it works, so a long turn shows up in its topic before the done
// post. It follows each agent's exact session transcript from a byte
// offset: text written while the agent is idle, or during a turn too short
// to be seen, is skipped, because the done post covers it. In Live mode one
// message per turn is edited as text arrives; in Messages mode each batch
// of new text is its own silent message. Progress runs on its own
// goroutine and owns all of its state.
type Progress struct {
	tg     domain.TelegramGateway
	src    domain.ProgressSource
	live   func() []domain.Agent
	topics func(domain.Key) (domain.TopicEntry, bool)
	opts   *Options
	clock  domain.Clock
	log    *slog.Logger

	states map[domain.Key]*progressState

	// Interval, Settle and Hold are the timings above; tests shorten them.
	Interval time.Duration
	Settle   time.Duration
	Hold     time.Duration
}

// progressState is what Progress knows about one agent.
type progressState struct {
	session string
	path    string
	offset  int64
	// retryAt is when an agent without a followable transcript is tried
	// again.
	retryAt time.Time

	inTurn     bool
	lastActive time.Time
	held       []heldText
	// msgID is the live message of the turn, 0 before the first post;
	// entries is what it shows and shown the Markdown it was last given.
	msgID   int
	entries []string
	shown   string
	posted  int
}

// heldText is text read but not posted yet; seen is when Progress read it,
// the age fallback for a record without a timestamp.
type heldText struct {
	domain.ProgressText
	seen time.Time
}

// NewProgress wires Progress over the redacting Telegram gateway, the
// transcript source, the registry's live agents and the topic read model.
func NewProgress(tg domain.TelegramGateway, src domain.ProgressSource, live func() []domain.Agent,
	topics func(domain.Key) (domain.TopicEntry, bool), opts *Options, clock domain.Clock, log *slog.Logger) *Progress {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Progress{
		tg: tg, src: src, live: live, topics: topics, opts: opts, clock: clock, log: log,
		states:   map[domain.Key]*progressState{},
		Interval: progressInterval,
		Settle:   progressSettle,
		Hold:     progressHold,
	}
}

// Run reads the transcripts every Interval until ctx ends.
func (p *Progress) Run(ctx context.Context) {
	p.log.Info("progress started", slog.Int64("interval_ms", p.Interval.Milliseconds()))
	defer p.log.Info("progress stopped")
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.clock.After(p.Interval):
			p.tick(ctx)
		}
	}
}

// tick handles every live agent once. With progress off or sync paused
// nothing is followed and the state is dropped, so switching back on
// starts from the end of each transcript instead of posting a backlog.
func (p *Progress) tick(ctx context.Context) {
	mode := p.opts.PostsProgress()
	if mode == domain.ProgressOff || !p.opts.SyncEnabled() {
		if len(p.states) > 0 {
			p.log.Debug("progress idle, state dropped", slog.String("mode", string(mode)), slog.Int("agents", len(p.states)))
			p.states = map[domain.Key]*progressState{}
		}
		return
	}
	now := p.clock.Now()
	seen := map[domain.Key]bool{}
	for _, a := range p.live() {
		if ctx.Err() != nil {
			return
		}
		seen[a.Key] = true
		if err := p.agent(ctx, a, mode, now); err != nil && ctx.Err() == nil {
			p.log.Warn("progress failed", slog.String("key", a.Key.String()), slog.String("err", err.Error()))
		}
	}
	for key := range p.states {
		if !seen[key] {
			delete(p.states, key)
		}
	}
}

// agent advances one agent: it reads what the transcript gained since the
// last tick and, depending on the agent's status, posts it, keeps it for
// the turn or skips it.
func (p *Progress) agent(ctx context.Context, a domain.Agent, mode domain.ProgressMode, now time.Time) error {
	st := p.states[a.Key]
	if st == nil || st.session != a.SessionID || (st.path == "" && !now.Before(st.retryAt)) {
		next, err := p.follow(ctx, a, st, now)
		p.states[a.Key] = next
		if err != nil {
			return nil // logged in follow, retried after progressRetry
		}
		st = next
	}
	if st.path == "" {
		return nil
	}
	texts, offset, err := p.src.ProgressSince(ctx, st.path, st.offset)
	if err != nil {
		return err
	}
	st.offset = offset

	entry, ok := p.topics(a.Key)
	postable := ok && entry.ThreadID != 0 && !entry.Closed && !entry.Muted
	active := a.Status == domain.StatusWorking || a.Status == domain.StatusBlocked
	switch {
	case !postable:
		p.reset(st)
		return nil
	case active:
		if !st.inTurn {
			p.reset(st)
			st.inTurn = true
			p.log.Debug("progress turn started", slog.String("key", a.Key.String()))
		}
		st.lastActive = now
		p.hold(st, texts, now)
		return p.post(ctx, a.Key, entry.ThreadID, st, mode, p.due(st, now), false)
	case st.inTurn:
		p.hold(st, texts, now)
		if now.Sub(st.lastActive) < p.Settle {
			return nil
		}
		// The turn is over. The newest text is the reply the done post
		// carries; everything held before it goes out now.
		due := st.held
		st.held = nil
		if len(due) > 0 && p.doneCarriesReply() {
			due = due[:len(due)-1]
		}
		err := p.post(ctx, a.Key, entry.ThreadID, st, mode, due, true)
		p.log.Debug("progress turn ended", slog.String("key", a.Key.String()), slog.Int("posted", st.posted))
		p.reset(st)
		return err
	}
	return nil // idle outside a turn: the text belongs to the done post
}

// follow starts following the agent's transcript. A session first seen
// now starts at the end of its file; a session that replaced one Progress
// was already following (/clear) starts at its beginning, since all of it
// is new. An agent without a followable transcript is retried later.
func (p *Progress) follow(ctx context.Context, a domain.Agent, prev *progressState, now time.Time) (*progressState, error) {
	st := &progressState{session: a.SessionID}
	path, size, err := p.src.ProgressTranscript(ctx, a)
	if err != nil {
		st.retryAt = now.Add(progressRetry)
		if prev == nil || prev.session != a.SessionID || prev.path != "" {
			p.log.Info("progress unavailable", slog.String("key", a.Key.String()), slog.String("err", err.Error()))
		}
		return st, err
	}
	st.path = path
	st.offset = size
	if prev != nil && prev.path != "" && prev.path != path {
		st.offset = 0
	}
	p.log.Debug("progress following", slog.String("key", a.Key.String()), slog.Int64("offset", st.offset))
	return st, nil
}

// hold queues newly read text.
func (p *Progress) hold(st *progressState, texts []domain.ProgressText, now time.Time) {
	for _, t := range texts {
		st.held = append(st.held, heldText{ProgressText: t, seen: now})
	}
}

// due takes the held text old enough to post. Text is kept in order: a
// young text keeps every text after it back too.
func (p *Progress) due(st *progressState, now time.Time) []heldText {
	n := 0
	for _, h := range st.held {
		written := h.Written
		if written.IsZero() || written.After(now) {
			written = h.seen
		}
		if now.Sub(written) < p.Hold {
			break
		}
		n++
	}
	due := st.held[:n]
	st.held = append([]heldText(nil), st.held[n:]...)
	return due
}

// doneCarriesReply reports whether the done post will show the agent's
// last reply, so progress may leave it out.
func (p *Progress) doneCarriesReply() bool {
	return p.opts.PostsDone() != domain.DoneScreen && p.opts.MinTurn() == 0
}

// reset ends the turn state of an agent; the transcript position stays.
func (p *Progress) reset(st *progressState) {
	st.inTurn = false
	st.held = nil
	st.msgID = 0
	st.entries = nil
	st.shown = ""
	st.posted = 0
}

// post sends due text in the chosen mode. final marks the last call of a
// turn: the live message's header then says the turn is over.
func (p *Progress) post(ctx context.Context, key domain.Key, threadID int, st *progressState, mode domain.ProgressMode, due []heldText, final bool) error {
	if len(due) == 0 && !(final && st.msgID != 0) {
		return nil
	}
	if mode == domain.ProgressMessages {
		if len(due) == 0 {
			return nil
		}
		parts := make([]string, 0, len(due))
		for _, h := range due {
			parts = append(parts, progressEntry(h))
		}
		if _, err := p.tg.Send(ctx, domain.Outgoing{ThreadID: threadID, Text: strings.Join(parts, "\n\n"), Markdown: true, MaxParts: replyMaxParts}); err != nil {
			return fmt.Errorf("send progress: %w", err)
		}
		st.posted += len(due)
		p.log.Info("progress posted", slog.String("key", key.String()), slog.String("mode", string(mode)), slog.Int("texts", len(due)))
		return nil
	}
	for _, h := range due {
		entry := progressEntry(h)
		if st.msgID != 0 && utf8.RuneCountInString(liveText(append(st.entries, entry), false)) > progressBudget {
			// The message is full: close it and start the next one.
			if err := p.edit(ctx, st, true); err != nil {
				return err
			}
			st.msgID, st.entries, st.shown = 0, nil, ""
		}
		st.entries = append(st.entries, entry)
		st.posted++
		if st.msgID == 0 {
			text := liveText(st.entries, false)
			id, err := p.tg.Send(ctx, domain.Outgoing{ThreadID: threadID, Text: text, Markdown: true, MaxParts: 1})
			if err != nil {
				return fmt.Errorf("send progress: %w", err)
			}
			st.msgID, st.shown = id, text
		}
	}
	if err := p.edit(ctx, st, final); err != nil {
		return err
	}
	p.log.Info("progress posted", slog.String("key", key.String()), slog.String("mode", string(mode)),
		slog.Int("texts", len(due)), slog.Int("message_id", st.msgID), slog.Bool("final", final))
	return nil
}

// edit brings the live message up to date. A message the operator deleted
// is forgotten, and the next text starts a new one.
func (p *Progress) edit(ctx context.Context, st *progressState, final bool) error {
	if st.msgID == 0 || len(st.entries) == 0 {
		return nil
	}
	text := liveText(st.entries, final)
	if text == st.shown {
		return nil
	}
	err := p.tg.EditMarkdown(ctx, st.msgID, text)
	if errors.Is(err, domain.ErrMessageGone) {
		st.msgID, st.entries, st.shown = 0, nil, ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("edit progress: %w", err)
	}
	st.shown = text
	return nil
}

// progressEntry renders one text with the local time it was written,
// cut so that a single entry always fits one live message.
func progressEntry(h heldText) string {
	written := h.Written
	if written.IsZero() {
		written = h.seen
	}
	text := h.Text
	if max := progressBudget - 200; utf8.RuneCountInString(text) > max {
		text = string([]rune(text)[:max]) + "…"
	}
	return "_" + written.Local().Format("15:04") + "_\n" + text
}

// liveText is the Markdown of a live message: a header and the entries,
// newest last.
func liveText(entries []string, final bool) string {
	header := "**⏳ Working**"
	if final {
		header = "**Progress**"
	}
	if n := len(entries); n > 1 {
		header += fmt.Sprintf(" · %d updates", n)
	}
	return header + "\n\n" + strings.Join(entries, "\n\n---\n\n")
}
