package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// defaultMaxForward bounds one forward read. A turn that wrote more than
// this between two reads (huge tool results) loses its oldest part: the
// read starts this far before the end and skips the cut line.
const defaultMaxForward = 4 << 20

// ProgressTranscript finds the transcript of the agent's exact session.
// Unlike LastReply it never falls back to the newest file of the
// directory: progress posts run all turn long, and a guess between two
// sessions of one directory would stream another pane's work.
func (r *Reader) ProgressTranscript(ctx context.Context, agent domain.Agent) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if agent.Kind != kindClaude {
		return "", 0, fmt.Errorf("%w: unsupported agent %q", domain.ErrNoReply, agent.Kind)
	}
	if agent.SessionID == "" {
		return "", 0, fmt.Errorf("%w: Herdr reports no session for the pane", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return "", 0, fmt.Errorf("%w: home directory: %v", domain.ErrNoReply, err)
	}
	dir := filepath.Join(append([]string{home}, append(projectsDir, projectSlug(agent.Cwd))...)...)
	path, info, ok := sessionTranscript(home, dir, agent.SessionID)
	if !ok {
		return "", 0, fmt.Errorf("%w: no transcript for session %s", domain.ErrNoReply, agent.SessionID)
	}
	return path, info.Size(), nil
}

// ProgressSince reads the transcript forward from offset and returns the
// text parts of main-conversation assistant records, in file order. Only
// complete lines are read: the returned offset stops after the last
// newline, so a record Claude Code is still writing is read next time.
// A file shorter than offset (rewritten) is read from the start.
func (r *Reader) ProgressSince(ctx context.Context, path string, offset int64) ([]domain.ProgressText, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, offset, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, fmt.Errorf("%w: open transcript: %v", domain.ErrNoReply, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, fmt.Errorf("%w: stat transcript: %v", domain.ErrNoReply, err)
	}
	texts, next, stats, err := progressFrom(f, info.Size(), offset, r.maxForward())
	r.log.Debug("transcript progress read",
		slog.String("chosen", filepath.Base(path)), slog.Int64("from", offset), slog.Int64("to", next),
		slog.Int("lines", stats.lines), slog.Int("skipped_json", stats.skipped), slog.Int("texts", len(texts)))
	return texts, next, err
}

func (r *Reader) maxForward() int64 {
	if r.maxScan > 0 && r.maxScan < defaultMaxForward {
		return r.maxScan
	}
	return defaultMaxForward
}

// progressFrom is ProgressSince over an open transcript of size bytes.
func progressFrom(f io.ReaderAt, size, offset, budget int64) ([]domain.ProgressText, int64, scanStats, error) {
	var stats scanStats
	if offset > size || offset < 0 {
		offset = 0
	}
	skipCut := false
	if size-offset > budget {
		offset = size - budget
		skipCut = true
	}
	if offset == size {
		return nil, offset, stats, nil
	}
	buf := make([]byte, size-offset)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, offset, stats, fmt.Errorf("%w: read transcript: %v", domain.ErrNoReply, err)
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil, offset, stats, nil // no complete line yet
	}
	data := buf[:end]
	next := offset + int64(end) + 1
	if skipCut {
		if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
			data = data[cut+1:]
		} else {
			data = nil
		}
	}
	var texts []domain.ProgressText
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		stats.lines++
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			stats.skipped++
			continue
		}
		if rec.Type != "assistant" || rec.IsSidechain {
			continue
		}
		for _, text := range textParts(rec.Message.Content) {
			texts = append(texts, domain.ProgressText{Text: text, Written: parseStamp(rec.Timestamp)})
		}
	}
	return texts, next, stats, nil
}

// textParts returns every non-empty text part of an assistant content
// list, trimmed, in order; a plain string content counts as one part.
func textParts(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		if s = strings.TrimSpace(s); s != "" {
			return []string{s}
		}
		return nil
	}
	if raw[0] != '[' {
		return nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if p.Type != "text" {
			continue
		}
		if text := strings.TrimSpace(p.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}
