package domain

import (
	"reflect"
	"testing"
)

func TestImagePaths(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"plain", "Saved the shot to /Users/me/shot.png for you.", []string{"/Users/me/shot.png"}},
		{"home", "see ~/Pictures/a.webp", []string{"~/Pictures/a.webp"}},
		{"code span", "Screenshot: `/tmp/x/Before.JPG`", []string{"/tmp/x/Before.JPG"}},
		{"markdown image", "![after](/tmp/after.jpeg) and **/tmp/b.gif**", []string{"/tmp/after.jpeg", "/tmp/b.gif"}},
		{"label colon", "before:/tmp/a.png", []string{"/tmp/a.png"}},
		{"adjacent", "/tmp/a.png /tmp/b.png", []string{"/tmp/a.png", "/tmp/b.png"}},
		{"repeat", "/tmp/a.png then /tmp/a.png again", []string{"/tmp/a.png"}},
		{"line end", "first\n/tmp/a.png\nlast", []string{"/tmp/a.png"}},
		{"relative", "edited src/assets/logo.png", nil},
		{"url", "https://example.com/img/a.png", nil},
		{"longer word", "/tmp/a.pngx /tmp/b.png_old", nil},
		{"not image", "/tmp/a.pdf /tmp/b.txt", nil},
		{"space in path", "/tmp/my shot.png", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ImagePaths(c.text); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ImagePaths(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestImagePathsBounded(t *testing.T) {
	text := ""
	for i := range MaxImagePaths + 3 {
		text += " /tmp/" + string(rune('a'+i)) + ".png"
	}
	if got := ImagePaths(text); len(got) != MaxImagePaths {
		t.Fatalf("paths = %d, want %d", len(got), MaxImagePaths)
	}
}
