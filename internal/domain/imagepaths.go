package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxImagePaths bounds how many images one text may name; paths past it
// stay text.
const MaxImagePaths = 10

// imagePathCandidate matches a full path to an image file: absolute or
// under "~/", without spaces, ending in an image extension. ImagePaths
// checks the characters around it.
var imagePathCandidate = regexp.MustCompile("(?:~/|/)[^\\s\"'<>()\\[\\]`*|]*?\\.(?i:png|jpe?g|gif|webp)")

// ImagePaths returns the image files a text names by full path, such as
// /Users/me/shot.png or ~/shots/a.webp, in order and without repeats. A
// path must start the text or follow a space or opening punctuation, so
// a relative path (src/a.png) or a URL never counts. Whether the file
// exists is the caller's question.
func ImagePaths(text string) []string {
	var out []string
	seen := map[string]bool{}
	for pos := 0; pos < len(text) && len(out) < MaxImagePaths; {
		loc := imagePathCandidate.FindStringIndex(text[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		pos = start + 1
		if !pathBoundaryBefore(text[:start]) || !pathBoundaryAfter(text[end:]) {
			continue
		}
		path := text[start:end]
		if strings.HasPrefix(path, "//") {
			continue // a URL after its scheme
		}
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
		pos = end
	}
	return out
}

// pathBoundaryBefore reports whether a path may start after before: at the
// start, after a space, or after opening punctuation or Markdown.
func pathBoundaryBefore(before string) bool {
	r, _ := utf8.DecodeLastRuneInString(before)
	return r == utf8.RuneError || unicode.IsSpace(r) || strings.ContainsRune("([<\"'`*:", r)
}

// pathBoundaryAfter reports whether a path may end before after: at the
// end or before anything but a letter, digit or underscore.
func pathBoundaryAfter(after string) bool {
	r, _ := utf8.DecodeRuneInString(after)
	return r == utf8.RuneError || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

// ImageFile is an image file an agent named by path, read for posting.
// Path is absolute and cleaned; Modified tells two versions of one file
// apart.
type ImageFile struct {
	Path     string
	Modified time.Time
	Image    ProgressImage
}
