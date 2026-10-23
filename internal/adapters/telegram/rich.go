package telegram

import (
	"strings"
)

// Rich messages (Bot API 10.1, sendRichMessage) take the agent's Markdown
// almost as it is: Telegram's "rich Markdown" follows GitHub Flavored
// Markdown, so tables, headings, lists, quotes and code blocks render
// natively instead of through renderMarkdown's lossy HTML. It also has
// syntax agents never mean in prose, measured against the live API on
// 2026-10-22:
//
//   - <T> or <div> is read as an HTML tag and vanishes; a backslash does
//     not escape it, an entity (&lt;) does.
//   - $PATH and $HOME becomes a formula ($...$); \$ escapes it.
//   - ||text|| becomes a spoiler and ==text== a highlight; a backslash
//     before each character escapes them.
//
// escapeRich applies those escapes outside fenced code and inline code,
// which Telegram shows verbatim. Table rows keep their | separators, so a
// || in a row stays an empty cell.

// richTextMax is the split limit for a rich part, below Telegram's 32768
// characters per rich message so a footer and the escapes always fit.
const richTextMax = 30000

// richEscaper escapes what rich Markdown would read as markup in prose.
// & goes first so the entities it writes are not escaped again.
var richEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", "$", `\$`, "==", `\=\=`, "||", `\|\|`)

// richCellEscaper is richEscaper for a table row, where | is structure.
var richCellEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", "$", `\$`, "==", `\=\=`)

// escapeRich escapes the prose of one Markdown part for the markdown field
// of a rich message; fenced code blocks and inline code spans pass
// through unchanged.
func escapeRich(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	fence := ""
	inTable := false
	for i, line := range lines {
		if fence != "" {
			if m := mdFence.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], fence) && strings.TrimSpace(m[2]) == "" {
				fence = ""
			}
			continue
		}
		if m := mdFence.FindStringSubmatch(line); m != nil {
			fence = m[1]
			inTable = false
			continue
		}
		switch {
		case !strings.Contains(line, "|"):
			inTable = false
		case !inTable && i+1 < len(lines) && mdTableRule.MatchString(lines[i+1]):
			inTable = true
		}
		escaper := richEscaper
		if inTable {
			escaper = richCellEscaper
		}
		lines[i] = escapeOutsideCode(line, escaper)
	}
	return strings.Join(lines, "\n")
}

// escapeOutsideCode applies r to the parts of line outside `code` spans.
// An unmatched backtick escapes the rest of the line like prose.
func escapeOutsideCode(line string, r *strings.Replacer) string {
	var b strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			b.WriteString(r.Replace(line))
			return b.String()
		}
		run := start
		for run < len(line) && line[run] == '`' {
			run++
		}
		ticks := line[start:run]
		end := strings.Index(line[run:], ticks)
		if end < 0 {
			b.WriteString(r.Replace(line))
			return b.String()
		}
		b.WriteString(r.Replace(line[:start]))
		b.WriteString(line[start : run+end+len(ticks)])
		line = line[run+end+len(ticks):]
	}
}

// richFooter is the footer as a final paragraph of a rich part: escaped
// in full, since it is plain text.
func richFooter(footer string) string {
	if footer == "" {
		return ""
	}
	return "\n\n" + richEscaper.Replace(footer)
}
