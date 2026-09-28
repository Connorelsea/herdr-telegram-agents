package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
)

// Key identifies an agent for the lifetime of the plugin state.
//
// PaneID alone is not enough: a new agent can start in the same pane after the
// previous one exits. TerminalID identifies the current runtime instance;
// SessionDigest can identify that session across a Herdr restart.
type Key struct {
	PaneID        string
	TerminalID    string
	SessionDigest string
}

// String renders a sessionless key as "<pane>/<terminal>" and a sessioned
// key as a versioned, delimiter-safe value for logs and state files.
func (k Key) String() string {
	if k.SessionDigest != "" {
		return "v2:" + encodeKeyPart(k.PaneID) + ":" + encodeKeyPart(k.TerminalID) + ":" + k.SessionDigest
	}
	return k.PaneID + "/" + k.TerminalID
}

// SessionTuple is the complete Herdr session identity used to derive a key.
// Keep it transient: persist only Digest(), never the tuple values.
type SessionTuple struct {
	Source string
	Agent  string
	Kind   string
	Value  string
}

// Digest returns a deterministic SHA-256 digest for a complete session tuple.
// An incomplete tuple has no usable identity and returns an empty digest.
func (s SessionTuple) Digest() string {
	parts := [...]string{s.Source, s.Agent, s.Kind, s.Value}
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}

	h := sha256.New()
	var size [binary.MaxVarintLen64]byte
	for _, part := range parts {
		n := binary.PutUvarint(size[:], uint64(len(part)))
		_, _ = h.Write(size[:n])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SameSession reports whether two keys identify the same session in the same
// pane. Terminal IDs are intentionally ignored because they can change across
// a Herdr restart.
func (k Key) SameSession(other Key) bool {
	return k.PaneID != "" && k.PaneID == other.PaneID &&
		k.SessionDigest != "" && k.SessionDigest == other.SessionDigest
}

// ConflictsWith reports whether two keys in the same pane carry different,
// known session identities.
func (k Key) ConflictsWith(other Key) bool {
	return k.PaneID != "" && k.PaneID == other.PaneID &&
		k.SessionDigest != "" && other.SessionDigest != "" && k.SessionDigest != other.SessionDigest
}

// SameIdentity reports whether two keys have a known matching identity. Two
// session-aware keys compare by digest; if either has no digest, the current
// terminal ID is the only exact identity available.
func (k Key) SameIdentity(other Key) bool {
	if k.PaneID == "" || k.PaneID != other.PaneID {
		return false
	}
	if k.SessionDigest != "" && other.SessionDigest != "" {
		return k.SessionDigest == other.SessionDigest
	}
	return k.TerminalID != "" && k.TerminalID == other.TerminalID
}

func encodeKeyPart(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// Agent is the plugin's view of one Herdr agent.
//
// Title is the pane title as reported by Herdr with the spinner already
// stripped by the adapter, so the domain treats it as clean text.
type Agent struct {
	Key
	WorkspaceID    string
	WorkspaceLabel string
	TabID          string
	TabLabel       string
	Kind           string
	Name           string
	Title          string
	Status         Status
	Revision       int64
	StateChangeSeq int64
	Focused        bool
	// Cwd is the working directory Herdr reports for the pane; the reply
	// source uses it to find the agent's own transcript. Empty when Herdr
	// does not know it.
	Cwd string
	// SessionID is the agent's own session id when Herdr's integration
	// reports one (Claude Code: the transcript file name). It names the
	// exact transcript, where Cwd can only guess between the sessions of
	// one directory. Held in memory only; never persisted.
	SessionID string
}

// labelSeparator joins the workspace and the agent part of a label, the
// way Herdr's Agents panel does.
const labelSeparator = " · "

// Label is the topic name for the agent, mirroring the row Herdr shows in
// its Agents panel: "<workspace> · <agent>". The workspace part is the
// workspace label, else its id. The agent part is the custom agent name,
// else the tab label, else the agent kind. Missing parts are dropped, so a
// bare agent name or a bare workspace is still a usable label. The terminal
// title is deliberately not used: agents rewrite it for every task and a
// topic name should stay put.
func (a Agent) Label() string {
	ws := a.WorkspaceLabel
	if ws == "" {
		ws = a.WorkspaceID
	}
	who := a.Name
	if who == "" {
		who = a.TabLabel
	}
	if who == "" {
		who = a.Kind
	}
	switch {
	case ws == "":
		return who
	case who == "":
		return ws
	}
	return ws + labelSeparator + who
}

// Workspace is one Herdr workspace as /new sees it: the id the socket
// wants and the label the operator types.
type Workspace struct {
	ID    string
	Label string
}

// Tab is one Herdr tab as CreateTab returns it: the ids the socket wants,
// the label Herdr chose and the root pane an agent may be started in.
type Tab struct {
	ID          string
	WorkspaceID string
	Label       string
	RootPaneID  string
}
