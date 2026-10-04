package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// sessionServer is a fake Herdr server serving one agent in pane w1:p1.
func sessionServer(t *testing.T, workspace string) *testkit.NDJSONServer {
	t.Helper()
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", pingHandler)
	s.Handle("agent.list", func(string, json.RawMessage) (any, *testkit.APIError) {
		return json.RawMessage(`{"type":"agent_list","agents":[{"agent":"claude","agent_status":"idle","pane_id":"w1:p1","tab_id":"w1:t1","terminal_id":"term-1","workspace_id":"w1"}]}`), nil
	})
	s.Handle("workspace.list", func(string, json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "workspace_list", "workspaces": []map[string]any{{"workspace_id": "w1", "label": workspace}}}, nil
	})
	s.Handle("tab.list", func(string, json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "tab_list", "tabs": []map[string]any{{"tab_id": "w1:t1", "workspace_id": "w1", "label": "claude"}}}, nil
	})
	s.Handle("agent.prompt", ackHandler)
	return s
}

// finder is a SocketFinder whose sessions a test can change.
type finder struct {
	mu    sync.Mutex
	socks map[string]string
}

func (f *finder) find() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.socks))
	for k, v := range f.socks {
		out[k] = v
	}
	return out
}

func (f *finder) set(name, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == "" {
		delete(f.socks, name)
		return
	}
	f.socks[name] = path
}

func newMulti(t *testing.T, f *finder) *MultiGateway {
	t.Helper()
	m := NewMultiGateway(f.find, testLogger(t), fastBackoff)
	m.poll = 20 * time.Millisecond
	if err := m.Start(ctxT(t)); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestQualifyAndSplitID(t *testing.T) {
	if got := QualifyID("", "w1:p1"); got != "w1:p1" {
		t.Fatalf("default session qualified: %q", got)
	}
	if got := QualifyID("agentArt", ""); got != "" {
		t.Fatalf("empty id qualified: %q", got)
	}
	id := QualifyID("agentArt", "w1:p1")
	if id != "agentArt|w1:p1" {
		t.Fatalf("QualifyID = %q", id)
	}
	if s, raw := SplitID(id); s != "agentArt" || raw != "w1:p1" {
		t.Fatalf("SplitID = %q %q", s, raw)
	}
	if s, raw := SplitID("w1:p1"); s != "" || raw != "w1:p1" {
		t.Fatalf("SplitID default = %q %q", s, raw)
	}
	// A qualified key still round-trips through the mapping's key format.
	k := domain.Key{PaneID: id, TerminalID: "term-1"}
	if got, ok := domain.ParseKey(k.String()); !ok || got != k {
		t.Fatalf("ParseKey(%q) = %+v %v", k.String(), got, ok)
	}
}

func TestSessionSockets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"agentArt", "stale"} {
		if err := os.MkdirAll(filepath.Join(root, "sessions", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", "agentArt", "herdr.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"":         filepath.Join(root, "herdr.sock"),
		"agentArt": filepath.Join(root, "sessions", "agentArt", "herdr.sock"),
	}
	// The root is the same whichever session's socket the daemon was given.
	for _, from := range []string{want[""], want["agentArt"]} {
		if got := SessionSockets(from)(); !reflect.DeepEqual(got, want) {
			t.Fatalf("SessionSockets(%s) = %v, want %v", from, got, want)
		}
	}
}

func TestMultiGatewayMergesAndRoutes(t *testing.T) {
	def := sessionServer(t, "Work")
	art := sessionServer(t, "Art")
	m := newMulti(t, &finder{socks: map[string]string{"": def.Path(), "agentArt": art.Path()}})

	agents, err := m.ListAgents(ctxT(t))
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var panes, labels []string
	for _, a := range agents {
		panes = append(panes, a.PaneID+" "+a.TabID+" "+a.WorkspaceID)
		labels = append(labels, a.WorkspaceLabel)
	}
	if want := []string{"w1:p1 w1:t1 w1", "agentArt|w1:p1 agentArt|w1:t1 agentArt|w1"}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes = %v, want %v", panes, want)
	}
	if want := []string{"Work", "Art"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}

	if err := m.Prompt(ctxT(t), "agentArt|w1:p1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := lastParams(t, art, "agent.prompt"); got["target"] != "w1:p1" {
		t.Fatalf("named session prompt params = %v", got)
	}
	if len(def.WaitRequests("agent.prompt", 1, 50*time.Millisecond)) != 0 {
		t.Fatal("default session received the named session's prompt")
	}
	if err := m.Prompt(ctxT(t), "ghost|w1:p1", "hi"); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("unknown session prompt err = %v, want ErrAgentGone", err)
	}

	ws, err := m.ListWorkspaces(ctxT(t))
	if err != nil || len(ws) != 2 || ws[1].ID != "agentArt|w1" {
		t.Fatalf("ListWorkspaces = %+v %v", ws, err)
	}
}

func TestMultiGatewayQualifiesEventsAndSplitsWatch(t *testing.T) {
	def := sessionServer(t, "Work")
	art := sessionServer(t, "Art")
	m := newMulti(t, &finder{socks: map[string]string{"": def.Path(), "agentArt": art.Path()}})
	for _, s := range []*testkit.NDJSONServer{def, art} {
		if len(s.WaitRequests("events.subscribe", 1, 2*time.Second)) < 1 {
			t.Fatal("stream did not subscribe")
		}
	}

	art.Push("pane_closed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"})
	if ev := next(t, m.Events()); ev.Kind != domain.PaneClosed || ev.PaneID != "agentArt|w1:p1" || ev.WorkspaceID != "agentArt|w1" {
		t.Fatalf("named session event = %+v", ev)
	}
	def.Push("pane_closed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"})
	if ev := next(t, m.Events()); ev.PaneID != "w1:p1" {
		t.Fatalf("default session event = %+v", ev)
	}

	if err := m.WatchPanes(ctxT(t), []string{"w1:p1", "agentArt|w2:p2"}); err != nil {
		t.Fatalf("WatchPanes: %v", err)
	}
	reqs := art.WaitRequests("events.subscribe", 2, 2*time.Second)
	if len(reqs) != 2 {
		t.Fatalf("named session resubscribes = %d, want 2", len(reqs))
	}
	var panes []string
	for _, sub := range subscribedTypes(t, reqs[1]) {
		if sub.Type == "pane.agent_status_changed" {
			panes = append(panes, sub.PaneID)
		}
	}
	if want := []string{"w2:p2"}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("named session watched = %v, want %v", panes, want)
	}
}

func TestMultiGatewayFollowsSessionsComingAndGoing(t *testing.T) {
	def := sessionServer(t, "Work")
	f := &finder{socks: map[string]string{"": def.Path()}}
	m := newMulti(t, f)

	art := sessionServer(t, "Art")
	f.set("agentArt", art.Path())
	waitReset(t, m)
	agents, err := m.ListAgents(ctxT(t))
	if err != nil || len(agents) != 2 {
		t.Fatalf("after session start: %d agents, err %v", len(agents), err)
	}

	f.set("agentArt", "")
	waitReset(t, m)
	agents, err = m.ListAgents(ctxT(t))
	if err != nil || len(agents) != 1 || agents[0].PaneID != "w1:p1" {
		t.Fatalf("after session stop: %+v, err %v", agents, err)
	}
}

func TestMultiGatewayDropsNamedSessionThatStopsAnswering(t *testing.T) {
	def := sessionServer(t, "Work")
	art := sessionServer(t, "Art")
	m := newMulti(t, &finder{socks: map[string]string{"": def.Path(), "agentArt": art.Path()}})

	art.Close()
	agents, err := m.ListAgents(ctxT(t))
	if err != nil {
		t.Fatalf("ListAgents with a dead named session: %v", err)
	}
	if len(agents) != 1 || agents[0].PaneID != "w1:p1" {
		t.Fatalf("agents = %+v, want only the default session's", agents)
	}
}

func TestMultiGatewayStartsWithoutDefaultSession(t *testing.T) {
	art := sessionServer(t, "Art")
	m := newMulti(t, &finder{socks: map[string]string{"": filepath.Join(t.TempDir(), "herdr.sock"), "agentArt": art.Path()}})
	agents, err := m.ListAgents(ctxT(t))
	if err != nil || len(agents) != 1 || agents[0].PaneID != "agentArt|w1:p1" {
		t.Fatalf("agents = %+v, err %v", agents, err)
	}
}

func TestMultiGatewayStartFailsWithNoSession(t *testing.T) {
	m := NewMultiGateway((&finder{socks: map[string]string{"": filepath.Join(t.TempDir(), "herdr.sock")}}).find, testLogger(t), fastBackoff)
	if err := m.Start(ctxT(t)); err == nil {
		_ = m.Close()
		t.Fatal("Start succeeded with no reachable session")
	}
}

// waitReset waits for the StreamReset that announces a session change.
func waitReset(t *testing.T, m *MultiGateway) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-m.Events():
			if he, ok := ev.(domain.HerdrEvent); ok && he.Kind == domain.StreamReset && he.PaneID == "" {
				return
			}
		case <-deadline:
			t.Fatal("no StreamReset after the session change")
		}
	}
}
