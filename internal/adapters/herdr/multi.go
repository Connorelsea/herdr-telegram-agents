package herdr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// SessionSeparator joins a named session to the Herdr id it qualifies:
// "agentArt|w2:p1". Ids from the default session carry no prefix, so
// mappings written before sessions were followed keep their keys. "|" is
// outside Herdr's session-name alphabet and does not collide with the "/"
// of a sessionless domain.Key.
const SessionSeparator = "|"

// sessionPoll is how often the multi gateway looks for sessions that
// started or stopped.
const sessionPoll = 5 * time.Second

// SocketFinder returns every candidate Herdr socket by session name; ""
// is the default session. A listed socket may be stale; the multi gateway
// keeps only the ones that answer a ping.
type SocketFinder func() map[string]string

// MultiGateway follows every Herdr session at once: the default server and
// each named one (`herdr --session NAME`). Herdr's ids are per server, so
// a named session's pane, tab and workspace ids are qualified with
// "NAME|"; calls are routed back by that prefix and the prefix is stripped
// before they reach the socket. Sessions are found on Start and then
// polled, and each arrival or departure is announced as a StreamReset so
// the application reconciles.
type MultiGateway struct {
	find    SocketFinder
	log     *slog.Logger
	backoff Backoff
	poll    time.Duration

	mu      sync.RWMutex
	gws     map[string]*Gateway
	watched []string

	events chan domain.Event

	runMu  sync.Mutex
	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ domain.HerdrGateway = (*MultiGateway)(nil)

// NewMultiGateway prepares a gateway over every socket find reports; call
// Start to connect. A zero Backoff falls back to DefaultBackoff.
func NewMultiGateway(find SocketFinder, log *slog.Logger, backoff Backoff) *MultiGateway {
	if backoff == (Backoff{}) {
		backoff = DefaultBackoff
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &MultiGateway{
		find:    find,
		log:     log,
		backoff: backoff,
		poll:    sessionPoll,
		gws:     map[string]*Gateway{},
		events:  make(chan domain.Event, eventBuffer),
	}
}

// sessionSocket matches a named session's socket path and captures the
// Herdr config root and the session name.
var sessionSocket = regexp.MustCompile(`^(.*)/sessions/([^/]+)/herdr\.sock$`)

// SessionSockets finds sockets in the Herdr config root that holds
// socketPath, whichever session socketPath belongs to: root/herdr.sock is
// the default session and root/sessions/NAME/herdr.sock a named one.
func SessionSockets(socketPath string) SocketFinder {
	root := filepath.Dir(socketPath)
	if m := sessionSocket.FindStringSubmatch(filepath.ToSlash(socketPath)); m != nil {
		root = filepath.FromSlash(m[1])
	}
	return func() map[string]string {
		out := map[string]string{"": filepath.Join(root, "herdr.sock")}
		entries, err := os.ReadDir(filepath.Join(root, "sessions"))
		if err != nil {
			return out
		}
		for _, e := range entries {
			if !e.IsDir() || strings.Contains(e.Name(), SessionSeparator) {
				continue
			}
			sock := filepath.Join(root, "sessions", e.Name(), "herdr.sock")
			if _, err := os.Stat(sock); err == nil {
				out[e.Name()] = sock
			}
		}
		return out
	}
}

// SessionOf names the session socketPath belongs to: a named session's
// socket (root/sessions/NAME/herdr.sock) gives NAME, anything else the
// default session "".
func SessionOf(socketPath string) string {
	if m := sessionSocket.FindStringSubmatch(filepath.ToSlash(socketPath)); m != nil {
		return m[2]
	}
	return ""
}

// QualifyID prefixes a Herdr id from session with the session name; ids of
// the default session and empty ids are returned unchanged.
func QualifyID(session, id string) string {
	if session == "" || id == "" {
		return id
	}
	return session + SessionSeparator + id
}

// SplitID undoes QualifyID: the session name ("" for the default session)
// and the id Herdr knows.
func SplitID(id string) (session, raw string) {
	if s, r, ok := strings.Cut(id, SessionSeparator); ok {
		return s, r
	}
	return "", id
}

// Start connects to every session that answers and starts polling for
// sessions that come and go. It fails only when no session answers, so a
// missing Herdr is reported right away as before.
func (m *MultiGateway) Start(ctx context.Context) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.cancel != nil {
		return errors.New("herdr gateway already started")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.runCtx, m.cancel = runCtx, cancel
	var firstErr error
	m.sync(ctx, func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	})
	if m.count() == 0 {
		cancel()
		m.cancel = nil
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: no herdr session found", domain.ErrDisconnected)
		}
		return firstErr
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(m.poll)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if m.sync(runCtx, nil) {
					m.emit(domain.HerdrEvent{Kind: domain.StreamReset})
				}
			}
		}
	}()
	return nil
}

// sync connects to sessions that appeared and drops named sessions whose
// socket is gone; it reports whether the set changed. fail receives each
// connection error when not nil.
func (m *MultiGateway) sync(ctx context.Context, fail func(error)) bool {
	found := m.find()
	changed := false
	for name, path := range found {
		if m.has(name) {
			continue
		}
		g := NewGateway(path, m.log.With(slog.String("herdr_session", sessionLabel(name))), m.backoff)
		if err := g.Start(ctx); err != nil {
			m.log.Debug("herdr session not reachable", slog.String("herdr_session", sessionLabel(name)), slog.String("err", err.Error()))
			if fail != nil {
				fail(err)
			}
			continue
		}
		m.add(name, g)
		changed = true
	}
	for _, name := range m.names() {
		if _, ok := found[name]; !ok && name != "" {
			m.drop(name, "socket gone")
			changed = true
		}
	}
	return changed
}

func (m *MultiGateway) add(name string, g *Gateway) {
	m.mu.Lock()
	if m.runCtx == nil || m.runCtx.Err() != nil {
		m.mu.Unlock()
		_ = g.Close()
		return
	}
	m.gws[name] = g
	panes := panesOf(name, m.watched)
	m.wg.Add(1)
	m.mu.Unlock()
	_ = g.WatchPanes(context.Background(), panes)
	go func() {
		defer m.wg.Done()
		for ev := range g.Events() {
			m.emit(qualifyEvent(name, ev))
		}
	}()
	m.log.Info("herdr session followed", slog.String("herdr_session", sessionLabel(name)))
}

// drop disconnects a session; its agents vanish from the next listing.
func (m *MultiGateway) drop(name, reason string) {
	m.mu.Lock()
	g, ok := m.gws[name]
	delete(m.gws, name)
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = g.Close()
	m.log.Info("herdr session dropped", slog.String("herdr_session", sessionLabel(name)), slog.String("reason", reason))
}

func (m *MultiGateway) emit(ev domain.Event) {
	select {
	case m.events <- ev:
	case <-m.runCtx.Done():
	}
}

// Close disconnects every session, waits for the forwarders and closes the
// events channel. It is safe to call more than once.
func (m *MultiGateway) Close() error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	m.cancel = nil
	m.mu.Lock()
	gws := m.gws
	m.gws = map[string]*Gateway{}
	m.mu.Unlock()
	for _, g := range gws {
		_ = g.Close()
	}
	m.wg.Wait()
	close(m.events)
	return nil
}

// Events returns the merged stream of every session; it is closed by Close.
func (m *MultiGateway) Events() <-chan domain.Event { return m.events }

// WatchPanes hands each session the panes that belong to it.
func (m *MultiGateway) WatchPanes(ctx context.Context, paneIDs []string) error {
	m.mu.Lock()
	m.watched = slices.Clone(paneIDs)
	gws := maps.Clone(m.gws)
	m.mu.Unlock()
	for name, g := range gws {
		_ = g.WatchPanes(ctx, panesOf(name, paneIDs))
	}
	return nil
}

// ListAgents lists every session's agents. A failing default session fails
// the listing as a lone gateway would; a named session that has gone away
// is dropped instead, so its agents read as exited rather than stalling
// every pass.
func (m *MultiGateway) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	type result struct {
		name   string
		agents []domain.Agent
		err    error
	}
	gws := m.snapshot()
	results := make([]result, len(gws))
	var wg sync.WaitGroup
	for n, s := range gws {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agents, err := s.g.ListAgents(ctx)
			results[n] = result{s.name, agents, err}
		}()
	}
	wg.Wait()
	var out []domain.Agent
	for _, r := range results {
		if r.err != nil {
			if r.name != "" && errors.Is(r.err, domain.ErrDisconnected) {
				m.drop(r.name, r.err.Error())
				continue
			}
			return nil, r.err
		}
		for _, a := range r.agents {
			out = append(out, qualifyAgent(r.name, a))
		}
	}
	if out == nil {
		out = []domain.Agent{}
	}
	return out, nil
}

// ListWorkspaces lists every session's workspaces under qualified ids.
func (m *MultiGateway) ListWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	var out []domain.Workspace
	for _, s := range m.snapshot() {
		ws, err := s.g.ListWorkspaces(ctx)
		if err != nil {
			if s.name != "" {
				continue
			}
			return nil, err
		}
		for _, w := range ws {
			w.ID = QualifyID(s.name, w.ID)
			w.Session = s.name
			out = append(out, w)
		}
	}
	if out == nil {
		out = []domain.Workspace{}
	}
	return out, nil
}

// AgentSession routes to the session that owns paneID.
func (m *MultiGateway) AgentSession(ctx context.Context, paneID string) (domain.SessionTuple, error) {
	g, raw, err := m.route(paneID)
	if err != nil {
		return domain.SessionTuple{}, nil
	}
	return g.AgentSession(ctx, raw)
}

func (m *MultiGateway) ReadScreen(ctx context.Context, target string, source domain.ScreenSource, lines int) (domain.Screen, error) {
	g, raw, err := m.route(target)
	if err != nil {
		return domain.Screen{}, err
	}
	return g.ReadScreen(ctx, raw, source, lines)
}

func (m *MultiGateway) ReadLimit(ctx context.Context, target string) (int, bool) {
	g, raw, err := m.route(target)
	if err != nil {
		return 0, false
	}
	return g.ReadLimit(ctx, raw)
}

func (m *MultiGateway) Prompt(ctx context.Context, target, text string) error {
	g, raw, err := m.route(target)
	if err != nil {
		return err
	}
	return g.Prompt(ctx, raw, text)
}

func (m *MultiGateway) SendKeys(ctx context.Context, target string, keys []string) error {
	g, raw, err := m.route(target)
	if err != nil {
		return err
	}
	return g.SendKeys(ctx, raw, keys)
}

func (m *MultiGateway) Rename(ctx context.Context, target string, name *string) error {
	g, raw, err := m.route(target)
	if err != nil {
		return err
	}
	return g.Rename(ctx, raw, name)
}

func (m *MultiGateway) RenameTab(ctx context.Context, tabID, label string) error {
	g, raw, err := m.route(tabID)
	if err != nil {
		return err
	}
	return g.RenameTab(ctx, raw, label)
}

func (m *MultiGateway) Focus(ctx context.Context, target string) error {
	g, raw, err := m.route(target)
	if err != nil {
		return err
	}
	return g.Focus(ctx, raw)
}

// CreateTab opens the tab in the session that owns the workspace.
func (m *MultiGateway) CreateTab(ctx context.Context, workspaceID string) (domain.Tab, error) {
	session, _ := SplitID(workspaceID)
	g, raw, err := m.route(workspaceID)
	if err != nil {
		return domain.Tab{}, err
	}
	tab, err := g.CreateTab(ctx, raw)
	if err != nil {
		return domain.Tab{}, err
	}
	tab.ID = QualifyID(session, tab.ID)
	tab.WorkspaceID = QualifyID(session, tab.WorkspaceID)
	tab.RootPaneID = QualifyID(session, tab.RootPaneID)
	return tab, nil
}

func (m *MultiGateway) StartAgent(ctx context.Context, name, kind, paneID string, timeout time.Duration) (domain.Agent, error) {
	session, _ := SplitID(paneID)
	g, raw, err := m.route(paneID)
	if err != nil {
		return domain.Agent{}, err
	}
	a, err := g.StartAgent(ctx, name, kind, raw, timeout)
	if err != nil {
		return domain.Agent{}, err
	}
	return qualifyAgent(session, a), nil
}

func (m *MultiGateway) ClosePane(ctx context.Context, paneID string) error {
	g, raw, err := m.route(paneID)
	if err != nil {
		return err
	}
	return g.ClosePane(ctx, raw)
}

// Notify shows the notification through the default session, or through
// the first named one while the default server is not running.
func (m *MultiGateway) Notify(ctx context.Context, title, body string, sound domain.NotifySound) error {
	gws := m.snapshot()
	if len(gws) == 0 {
		return fmt.Errorf("%w: no herdr session", domain.ErrDisconnected)
	}
	return gws[0].g.Notify(ctx, title, body, sound)
}

// route finds the session that owns id and returns its gateway with the
// id Herdr knows. An id of a session that is not followed is ErrAgentGone.
func (m *MultiGateway) route(id string) (*Gateway, string, error) {
	session, raw := SplitID(id)
	m.mu.RLock()
	g, ok := m.gws[session]
	m.mu.RUnlock()
	if !ok {
		return nil, "", fmt.Errorf("herdr session %s: %w", sessionLabel(session), domain.ErrAgentGone)
	}
	return g, raw, nil
}

type namedGateway struct {
	name string
	g    *Gateway
}

// snapshot returns the followed sessions, default first, then by name.
func (m *MultiGateway) snapshot() []namedGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]namedGateway, 0, len(m.gws))
	for name, g := range m.gws {
		out = append(out, namedGateway{name, g})
	}
	slices.SortFunc(out, func(a, b namedGateway) int { return strings.Compare(a.name, b.name) })
	return out
}

func (m *MultiGateway) has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.gws[name]
	return ok
}

func (m *MultiGateway) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.gws)
}

func (m *MultiGateway) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.gws))
	for name := range m.gws {
		out = append(out, name)
	}
	return out
}

// panesOf picks the panes of session from qualified ids, unqualified.
func panesOf(session string, paneIDs []string) []string {
	out := []string{}
	for _, id := range paneIDs {
		if s, raw := SplitID(id); s == session {
			out = append(out, raw)
		}
	}
	return out
}

func sessionLabel(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func qualifyAgent(session string, a domain.Agent) domain.Agent {
	a.PaneID = QualifyID(session, a.PaneID)
	a.WorkspaceID = QualifyID(session, a.WorkspaceID)
	a.TabID = QualifyID(session, a.TabID)
	return a
}

func qualifyEvent(session string, ev domain.Event) domain.Event {
	he, ok := ev.(domain.HerdrEvent)
	if !ok || session == "" {
		return ev
	}
	he.PaneID = QualifyID(session, he.PaneID)
	he.WorkspaceID = QualifyID(session, he.WorkspaceID)
	he.TabID = QualifyID(session, he.TabID)
	if he.Agent != nil {
		a := qualifyAgent(session, *he.Agent)
		he.Agent = &a
	}
	return he
}
