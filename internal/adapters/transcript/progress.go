package transcript

import (
	"bytes"
	"context"
	"encoding/base64"
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
// text parts of main-conversation assistant records and the images tool
// results returned to the agent (a screenshot it read), in file order. Only
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
		slog.Int("lines", stats.lines), slog.Int("skipped_json", stats.skipped), slog.Int("texts", len(texts)),
		slog.Int("images", countImages(texts)))
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
	// tools names the tool calls of this read by id, so an image result
	// can say which file it came from. A call read in an earlier batch
	// leaves its result with a generic name.
	tools := map[string]toolPart{}
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
		if rec.IsSidechain {
			continue
		}
		written := parseStamp(rec.Timestamp)
		switch rec.Type {
		case "assistant":
			for _, text := range textParts(rec.Message.Content) {
				texts = append(texts, domain.ProgressText{Text: text, Written: written})
			}
			if bytes.Contains(line, []byte(`"tool_use"`)) {
				for _, p := range toolParts(rec.Message.Content) {
					if p.Type == "tool_use" && p.ID != "" {
						tools[p.ID] = p
					}
				}
			}
		case "user":
			if !bytes.Contains(line, []byte(`"image"`)) {
				continue
			}
			for _, img := range resultImages(rec.Message.Content, tools) {
				texts = append(texts, domain.ProgressText{Image: img, Written: written})
			}
		}
	}
	return texts, next, stats, nil
}

// toolPart is a tool_use part of an assistant record or a tool_result
// part of a user record, decoded only as far as images need.
type toolPart struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Input     struct {
		FilePath string `json:"file_path"`
	} `json:"input"`
	Content json.RawMessage `json:"content"`
}

// imagePart is an image inside a tool result's content list.
type imagePart struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// toolParts decodes a content list into its tool parts; anything else
// decodes to nothing.
func toolParts(raw json.RawMessage) []toolPart {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var parts []toolPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

// resultImages returns the base64 images inside the tool results of a user
// record's content, in order. An image the user attached to a prompt is
// not a tool result and is skipped, as is an image that does not decode.
func resultImages(raw json.RawMessage, tools map[string]toolPart) []*domain.ProgressImage {
	var out []*domain.ProgressImage
	for _, p := range toolParts(raw) {
		if p.Type != "tool_result" {
			continue
		}
		content := bytes.TrimSpace(p.Content)
		if len(content) == 0 || content[0] != '[' {
			continue
		}
		var items []imagePart
		if err := json.Unmarshal(content, &items); err != nil {
			continue
		}
		for _, item := range items {
			if item.Type != "image" || item.Source.Type != "base64" || item.Source.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(item.Source.Data)
			if err != nil || len(data) == 0 {
				continue
			}
			out = append(out, &domain.ProgressImage{
				Name:      imageName(tools[p.ToolUseID], item.Source.MediaType),
				MediaType: item.Source.MediaType,
				Data:      data,
			})
		}
	}
	return out
}

// imageName names an image after the file a Read call opened, else after
// the tool that returned it (the last part of an MCP tool's name), with
// the extension of its media type.
func imageName(call toolPart, mediaType string) string {
	if call.Name == "Read" && call.Input.FilePath != "" {
		if base := filepath.Base(call.Input.FilePath); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	name := "image"
	if call.Name != "" {
		name = call.Name
		if i := strings.LastIndex(name, "__"); i >= 0 && i+2 < len(name) {
			name = name[i+2:]
		}
	}
	return name + imageExt(mediaType)
}

// imageExt is the file extension of an image media type.
func imageExt(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".png"
}

// countImages counts the images among read progress items.
func countImages(texts []domain.ProgressText) int {
	n := 0
	for _, t := range texts {
		if t.Image != nil {
			n++
		}
	}
	return n
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
