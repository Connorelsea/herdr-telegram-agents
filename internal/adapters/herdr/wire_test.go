package herdr

import (
	"encoding/json"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Verified output of `herdr agent list` on Herdr 0.7.5 (protocol 17).
const agentListSample = `{
  "type": "agent_list",
  "agents": [{
    "agent": "claude",
    "agent_status": "idle",
    "cwd": "/Users/alex/Projects/Work/v3laravel",
    "focused": false,
    "foreground_cwd": "/Users/alex/Projects/Work/v3laravel",
    "pane_id": "w3:p2",
    "revision": 172,
    "state_change_seq": 9,
    "tab_id": "w3:t2",
    "terminal_id": "term_65a77760e3b0a1",
    "terminal_title": "✳ Объяснение первого пункта",
    "terminal_title_stripped": "Объяснение первого пункта",
    "workspace_id": "w3"
  }]
}`

func TestDecodeAgentListSample(t *testing.T) {
	var res agentListResult
	if err := json.Unmarshal([]byte(agentListSample), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Type != "agent_list" || len(res.Agents) != 1 {
		t.Fatalf("decoded %+v", res)
	}
	a := toDomainAgent(res.Agents[0])
	want := domain.Agent{
		Key:            domain.Key{PaneID: "w3:p2", TerminalID: "term_65a77760e3b0a1"},
		WorkspaceID:    "w3",
		TabID:          "w3:t2",
		Kind:           "claude",
		Title:          "Объяснение первого пункта",
		Status:         domain.StatusIdle,
		Revision:       172,
		StateChangeSeq: 9,
		Cwd:            "/Users/alex/Projects/Work/v3laravel",
	}
	if a != want {
		t.Fatalf("toDomainAgent =\n %+v\nwant\n %+v", a, want)
	}
	if a.Label() != "w3 · claude" {
		t.Fatalf("Label() = %q", a.Label())
	}
}

func TestToDomainAgentSession(t *testing.T) {
	idSession := domain.SessionTuple{Source: "codex", Agent: "codex", Kind: "id", Value: "session-id-42"}
	pathSession := domain.SessionTuple{Source: "claude-code", Agent: "claude", Kind: "path", Value: "/private/session/abc"}
	tests := []struct {
		name       string
		json       string
		wantDigest string
	}{
		{
			name:       "kind id",
			json:       `{"pane_id":"p1","terminal_id":"term-old","agent_session":{"source":"codex","agent":"codex","kind":"id","value":"session-id-42"}}`,
			wantDigest: idSession.Digest(),
		},
		{
			name:       "kind path",
			json:       `{"pane_id":"p2","terminal_id":"term-path","agent_session":{"source":"claude-code","agent":"claude","kind":"path","value":"/private/session/abc"}}`,
			wantDigest: pathSession.Digest(),
		},
		{
			name:       "null session",
			json:       `{"pane_id":"p3","terminal_id":"term-null","agent_session":null}`,
			wantDigest: "",
		},
		{
			name:       "absent session",
			json:       `{"pane_id":"p4","terminal_id":"term-absent"}`,
			wantDigest: "",
		},
		{
			name:       "terminal changed",
			json:       `{"pane_id":"p1","terminal_id":"term-new","agent_session":{"source":"codex","agent":"codex","kind":"id","value":"session-id-42"}}`,
			wantDigest: idSession.Digest(),
		},
	}
	agents := make(map[string]domain.Agent, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var info agentInfo
			if err := json.Unmarshal([]byte(tt.json), &info); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			agent := toDomainAgent(info)
			if agent.SessionDigest != tt.wantDigest {
				t.Fatalf("SessionDigest = %q, want %q", agent.SessionDigest, tt.wantDigest)
			}
			agents[tt.name] = agent
		})
	}
	if !agents["kind id"].Key.SameSession(agents["terminal changed"].Key) {
		t.Fatal("same session should keep its identity when terminal_id changes")
	}
}

func TestToDomainAgentLabelPriority(t *testing.T) {
	name := "  reviewer "
	tests := []struct {
		name string
		in   agentInfo
		want string
	}{
		{"custom name wins", agentInfo{Name: &name, Title: "meta", TerminalTitle: "✳ term", Agent: "claude", WorkspaceID: "w1"}, "w1 · reviewer"},
		{"titles are ignored", agentInfo{Title: "meta", TerminalTitle: "✳ term", TerminalTitleStripped: "term", Agent: "claude", WorkspaceID: "w1"}, "w1 · claude"},
		{"kind at workspace", agentInfo{Agent: "codex", WorkspaceID: "w7"}, "w7 · codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toDomainAgent(tt.in).Label(); got != tt.want {
				t.Fatalf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"✳ Объяснение", "Объяснение"},
		{"⠋ working", "working"},
		{"⚙️ gear", "gear"},
		{"✳✳  double", "double"},
		{"  plain  ", "plain"},
		{"", ""},
		{"✳", ""},
		{"fix ✳ inside", "fix ✳ inside"},
	}
	for _, tt := range tests {
		if got := cleanTitle(tt.in); got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseStatusFromWire(t *testing.T) {
	var a agentInfo
	if err := json.Unmarshal([]byte(`{"agent_status":"blocked","name":null}`), &a); err != nil {
		t.Fatal(err)
	}
	if got := toDomainAgent(a).Status; got != domain.StatusBlocked {
		t.Fatalf("status = %q", got)
	}
	if a.Name != nil {
		t.Fatalf("null name decoded as %q", *a.Name)
	}
}

func TestSubscriptionOmitsEmptyPaneID(t *testing.T) {
	b, err := json.Marshal(subscribeParams{Subscriptions: []subscription{
		{Type: "pane.closed"},
		{Type: "pane.agent_status_changed", PaneID: "w1:p1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"subscriptions":[{"type":"pane.closed"},{"type":"pane.agent_status_changed","pane_id":"w1:p1"}]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}

func TestToDomainAgentSessionID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session *agentSessionInfo
		want    string
	}{
		{"id session", &agentSessionInfo{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: " ef5f7515 "}, "ef5f7515"},
		{"path session", &agentSessionInfo{Source: "codex", Agent: "codex", Kind: "path", Value: "/private/session/abc"}, ""},
		{"no session", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toDomainAgent(agentInfo{PaneID: "p1", TerminalID: "t1", Agent: "claude", AgentSession: tc.session})
			if got.SessionID != tc.want {
				t.Fatalf("SessionID = %q, want %q", got.SessionID, tc.want)
			}
		})
	}
}
