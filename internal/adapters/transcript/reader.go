// Package transcript reads an agent's last reply from the session
// transcript the agent writes for itself, outside Herdr. Herdr 0.7.5 does
// not tell which session a pane runs, so the reader finds the transcript by
// the pane's working directory: Claude Code keeps one directory per
// project under ~/.claude/projects and one .jsonl per session, and the
// newest file is the session that just finished. Two Claude panes in the
// same directory cannot be told apart; that limitation is documented and
// the caller falls back to the screen whenever the reader is unsure.
package transcript

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// kindClaude is the Herdr agent kind this reader understands.
	kindClaude = "claude"
	// transcriptSuffix is the extension of Claude Code session files.
	transcriptSuffix = ".jsonl"
	// defaultMaxScan bounds how many bytes are read from the end of a
	// transcript before giving up: a turn with huge tool results in it
	// costs one screen post, not a multi-megabyte parse.
	defaultMaxScan = 4 << 20
)

// projectsDir is Claude Code's transcript root, relative to the home.
var projectsDir = []string{".claude", "projects"}

// configDirGlob matches the config dirs of other Claude Code accounts run
// with CLAUDE_CONFIG_DIR=~/.claude-NAME; each keeps its own projects dir.
const configDirGlob = ".claude-*"

// projectRoots lists every transcript root: ~/.claude/projects first, then
// the projects dir of each ~/.claude-NAME config dir, by name.
func projectRoots(home string) []string {
	roots := []string{filepath.Join(append([]string{home}, projectsDir...)...)}
	extra, _ := filepath.Glob(filepath.Join(home, configDirGlob, projectsDir[1]))
	sort.Strings(extra)
	for _, dir := range extra {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			roots = append(roots, dir)
		}
	}
	return roots
}

// Reader implements domain.ReplySource for Claude Code transcripts.
type Reader struct {
	home    func() (string, error)
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
}

// NewReader returns a reader over the current user's home directory.
func NewReader(log *slog.Logger) *Reader {
	return newReader(os.UserHomeDir, time.Now, log)
}

// newReader takes the home and clock sources so tests can point the reader
// at a temporary directory.
func newReader(home func() (string, error), now func() time.Time, log *slog.Logger) *Reader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reader{home: home, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply finds the agent's transcript and returns the last text the
// agent wrote after the operator's last prompt. Every failure is
// domain.ErrNoReply wrapped with the reason; the reader logs the lookup at
// debug and leaves the one info line to the caller.
func (r *Reader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindClaude {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrNoReply, agent.Kind)
	}
	if strings.TrimSpace(agent.Cwd) == "" {
		return domain.Reply{}, fmt.Errorf("%w: agent has no working directory", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: home directory: %v", domain.ErrNoReply, err)
	}
	dir := filepath.Join(append([]string{home}, append(projectsDir, projectSlug(agent.Cwd))...)...)
	path, modTime, candidates, err := "", time.Time{}, 0, error(nil)
	exactPath, info, exact := sessionTranscript(home, dir, agent.SessionID)
	if exact {
		path, modTime, candidates = exactPath, info.ModTime(), 1
	} else {
		path, modTime, candidates, err = newestAcross(home, projectSlug(agent.Cwd))
	}
	if err != nil {
		return domain.Reply{}, err
	}
	age := r.now().Sub(modTime)
	r.log.Debug("transcript lookup",
		slog.String("pane", agent.PaneID), slog.String("cwd", agent.Cwd), slog.String("dir", dir),
		slog.Int("candidates", candidates), slog.String("chosen", filepath.Base(path)), slog.Int64("age_ms", age.Milliseconds()))
	text, turn, stats, err := lastReplyIn(path, r.maxScan)
	meta := turn.meta()
	turnDuration, _ := meta.Duration()
	r.log.Debug("transcript scanned",
		slog.String("chosen", filepath.Base(path)), slog.Int("lines", stats.lines),
		slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped), slog.Bool("found", err == nil),
		slog.String("model", meta.Model), slog.Int("output_tokens", meta.OutputTokens), slog.Int("files", len(meta.Files)),
		slog.Int64("turn_ms", turnDuration.Milliseconds()), slog.Bool("complete", turn.complete))
	if err != nil {
		return domain.Reply{}, err
	}
	if stats.readErr != nil {
		r.log.Debug("[FIX] transcript read failed after the reply was found, stats are partial",
			slog.String("chosen", filepath.Base(path)), slog.Int64("bytes", stats.bytes), slog.String("err", stats.readErr.Error()))
	}
	return domain.Reply{Text: text, Source: path, Age: age, Written: modTime, Exact: exact, Meta: meta}, nil
}

// sessionTranscript finds the transcript of one known session: <id>.jsonl
// in the pane's project directory, or in any project directory when the
// session started elsewhere (the pane's cwd moved since), under any
// account's transcript root. ok is false for an unknown id or a missing
// file.
func sessionTranscript(home, dir, sessionID string) (string, os.FileInfo, bool) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) || strings.HasPrefix(sessionID, ".") {
		return "", nil, false
	}
	name := sessionID + transcriptSuffix
	if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
		return filepath.Join(dir, name), info, true
	}
	for _, root := range projectRoots(home) {
		matches, _ := filepath.Glob(filepath.Join(root, "*", name))
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() {
				return m, info, true
			}
		}
	}
	return "", nil, false
}

// newestAcross is newestTranscript over the project dir named slug under
// every account's transcript root: the newest file wins and the candidates
// add up. It fails with the first root's error when no root has one.
func newestAcross(home, slug string) (path string, modTime time.Time, candidates int, err error) {
	var firstErr error
	for _, root := range projectRoots(home) {
		p, mod, n, err := newestTranscript(filepath.Join(root, slug))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		candidates += n
		if path == "" || mod.After(modTime) {
			path, modTime = p, mod
		}
	}
	if path == "" {
		return "", time.Time{}, 0, firstErr
	}
	return path, modTime, candidates, nil
}

// newestTranscript returns the most recently modified session file in dir
// and how many candidates there were.
func newestTranscript(dir string) (path string, modTime time.Time, candidates int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", time.Time{}, 0, fmt.Errorf("%w: no transcript directory %s", domain.ErrNoReply, dir)
		}
		return "", time.Time{}, 0, fmt.Errorf("%w: read %s: %v", domain.ErrNoReply, dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), transcriptSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates++
		if path == "" || info.ModTime().After(modTime) {
			path, modTime = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if path == "" {
		return "", time.Time{}, 0, fmt.Errorf("%w: no transcript files in %s", domain.ErrNoReply, dir)
	}
	return path, modTime, candidates, nil
}
