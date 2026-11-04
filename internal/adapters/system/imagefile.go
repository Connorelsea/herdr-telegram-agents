package system

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// maxImageFile is the largest image file read for posting, below the
// 50 MB Telegram accepts from a bot as a file.
const maxImageFile = 20 << 20

// imageTypes maps the extensions ImageFiles reads to their media type.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// ImageFiles implements domain.ImageFiles over the local file system.
type ImageFiles struct {
	home func() (string, error)
	max  int64
	log  *slog.Logger
}

var _ domain.ImageFiles = (*ImageFiles)(nil)

// NewImageFiles reads image files from the local disk.
func NewImageFiles(log *slog.Logger) *ImageFiles {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ImageFiles{home: os.UserHomeDir, max: maxImageFile, log: log}
}

// ReadImage reads the image file at path; a leading "~/" is the home
// directory and a relative path is refused.
func (f *ImageFiles) ReadImage(ctx context.Context, path string) (domain.ImageFile, error) {
	if err := ctx.Err(); err != nil {
		return domain.ImageFile{}, err
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := f.home()
		if err != nil {
			return domain.ImageFile{}, fmt.Errorf("image %s: home directory: %w", path, err)
		}
		path = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(path) {
		return domain.ImageFile{}, fmt.Errorf("image %s: not a full path", path)
	}
	path = filepath.Clean(path)
	mediaType, ok := imageTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return domain.ImageFile{}, fmt.Errorf("image %s: not a png, jpg, gif or webp file", path)
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return domain.ImageFile{}, fmt.Errorf("image %s: no such file", path)
	case err != nil:
		return domain.ImageFile{}, fmt.Errorf("image %s: %w", path, err)
	case !info.Mode().IsRegular():
		return domain.ImageFile{}, fmt.Errorf("image %s: not a regular file", path)
	case info.Size() == 0:
		return domain.ImageFile{}, fmt.Errorf("image %s: empty file", path)
	case info.Size() > f.max:
		return domain.ImageFile{}, fmt.Errorf("image %s: %d MB is over the %d MB limit", path, info.Size()>>20, f.max>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.ImageFile{}, fmt.Errorf("image %s: %w", path, err)
	}
	f.log.Debug("image file read", slog.String("path", path), slog.Int("bytes", len(data)))
	return domain.ImageFile{
		Path:     path,
		Modified: info.ModTime(),
		Image:    domain.ProgressImage{Name: filepath.Base(path), MediaType: mediaType, Data: data},
	}, nil
}
