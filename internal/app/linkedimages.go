package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// linkedMemory bounds how many posted file versions are remembered per
// agent; past it the memory starts over.
const linkedMemory = 200

// linkedImages finds the image files an agent's posted text names by full
// path (posts.image_paths) and remembers which versions it handed out per
// agent, so a path named again for an unchanged file is not posted twice.
// Progress and the done post share one; it is safe for both goroutines.
type linkedImages struct {
	files domain.ImageFiles
	on    func() bool
	log   *slog.Logger

	mu     sync.Mutex
	posted map[domain.Key]map[string]time.Time
}

// newLinkedImages returns nil without files; a nil linkedImages finds
// nothing.
func newLinkedImages(files domain.ImageFiles, on func() bool, log *slog.Logger) *linkedImages {
	if files == nil {
		return nil
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &linkedImages{files: files, on: on, log: log, posted: map[domain.Key]map[string]time.Time{}}
}

// find returns the images text names that key has not posted yet, in the
// order named. A path that does not lead to a readable image stays text.
func (l *linkedImages) find(ctx context.Context, key domain.Key, text string) []domain.ImageFile {
	if l == nil || !l.on() {
		return nil
	}
	paths := domain.ImagePaths(text)
	if len(paths) == 0 {
		return nil
	}
	var out []domain.ImageFile
	for _, path := range paths {
		img, err := l.files.ReadImage(ctx, path)
		if err != nil {
			l.log.Debug("named image skipped", slog.String("key", key.String()), slog.String("err", err.Error()))
			continue
		}
		if !l.claim(key, img) {
			l.log.Debug("named image already posted", slog.String("key", key.String()), slog.String("path", img.Path))
			continue
		}
		out = append(out, img)
	}
	return out
}

// claim records img as posted for key and reports whether it was new.
func (l *linkedImages) claim(key domain.Key, img domain.ImageFile) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := l.posted[key]
	if seen == nil || len(seen) >= linkedMemory {
		seen = map[string]time.Time{}
		l.posted[key] = seen
	}
	if last, ok := seen[img.Path]; ok && last.Equal(img.Modified) {
		return false
	}
	seen[img.Path] = img.Modified
	return true
}
