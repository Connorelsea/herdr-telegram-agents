package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageFilesReadImage(t *testing.T) {
	home := t.TempDir()
	write := func(name string, size int) string {
		path := filepath.Join(home, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	shot := write("shot.PNG", 10)
	write("big.png", 30)
	write("empty.gif", 0)
	write("notes.txt", 5)
	if err := os.Mkdir(filepath.Join(home, "dir.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := NewImageFiles(nil)
	f.home = func() (string, error) { return home, nil }
	f.max = 20
	ctx := context.Background()

	img, err := f.ReadImage(ctx, shot)
	if err != nil {
		t.Fatalf("ReadImage: %v", err)
	}
	if img.Path != shot || img.Image.Name != "shot.PNG" || img.Image.MediaType != "image/png" || len(img.Image.Data) != 10 || img.Modified.IsZero() {
		t.Fatalf("ReadImage = %+v", img)
	}
	if img, err := f.ReadImage(ctx, "~/shot.PNG"); err != nil || img.Path != shot {
		t.Fatalf("ReadImage(~) = %+v, %v", img.Path, err)
	}
	for path, want := range map[string]string{
		"shot.png":                         "not a full path",
		filepath.Join(home, "missing.png"): "no such file",
		filepath.Join(home, "dir.png"):     "not a regular file",
		filepath.Join(home, "empty.gif"):   "empty file",
		filepath.Join(home, "big.png"):     "limit",
		filepath.Join(home, "notes.txt"):   "not a png",
	} {
		if _, err := f.ReadImage(ctx, path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadImage(%s) err = %v, want %q", path, err, want)
		}
	}
}
