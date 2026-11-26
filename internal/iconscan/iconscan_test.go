package iconscan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	icoBytes = []byte{0, 0, 1, 0, 1, 0, 16, 16, 0, 0}
	svgBytes = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
)

func write(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSelection(t *testing.T) {
	big := make([]byte, MaxBytes+1)
	copy(big, pngBytes)
	tests := []struct {
		name  string
		files map[string][]byte
		links map[string]string // link -> target (relative to root)
		want  string
		ctype string
	}{
		{name: "root favicon.ico", files: map[string][]byte{"favicon.ico": icoBytes}, want: "favicon.ico", ctype: "image/x-icon"},
		{name: "svg beats ico", files: map[string][]byte{"public/favicon.ico": icoBytes, "src/app/favicon.svg": svgBytes}, want: "src/app/favicon.svg", ctype: "image/svg+xml"},
		{name: "public preferred over static for the same name", files: map[string][]byte{"static/favicon.ico": icoBytes, "public/favicon.ico": icoBytes}, want: "public/favicon.ico"},
		{name: "apple touch icon beats ico", files: map[string][]byte{"public/favicon.ico": icoBytes, "public/apple-touch-icon.png": pngBytes}, want: "public/apple-touch-icon.png", ctype: "image/png"},
		{name: "favicon beats logo", files: map[string][]byte{"logo.svg": svgBytes, "assets/favicon.ico": icoBytes}, want: "assets/favicon.ico"},
		{name: "monorepo app", files: map[string][]byte{"apps/web/public/favicon.svg": svgBytes}, want: "apps/web/public/favicon.svg"},
		{name: "node_modules ignored", files: map[string][]byte{"node_modules/x/public/favicon.svg": svgBytes}},
		{name: "deep files ignored", files: map[string][]byte{"a/b/c/d/favicon.svg": svgBytes}},
		{name: "too large skipped", files: map[string][]byte{"public/favicon.png": big, "favicon.ico": icoBytes}, want: "favicon.ico"},
		{name: "not an image", files: map[string][]byte{"favicon.ico": []byte("<html>nope</html>")}},
		{name: "non-image names ignored", files: map[string][]byte{"public/secret.txt": []byte("x"), "public/favicon.txt": pngBytes}},
		{name: "symlinked file not followed", files: map[string][]byte{"outside/favicon.svg": svgBytes}, links: map[string]string{"public/favicon.svg": "outside/favicon.svg"}},
		{name: "symlinked dir not followed", files: map[string][]byte{"outside/favicon.svg": svgBytes}, links: map[string]string{"public": "outside"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, data := range tc.files {
				write(t, root, rel, data)
			}
			for link, target := range tc.links {
				lp := filepath.Join(root, filepath.FromSlash(link))
				if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, filepath.FromSlash(target)), lp); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Scan(root)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("Scan = %+v, want no result", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if got.RelPath != tc.want {
				t.Fatalf("RelPath = %q, want %q", got.RelPath, tc.want)
			}
			if tc.ctype != "" && got.ContentType != tc.ctype {
				t.Fatalf("ContentType = %q, want %q", got.ContentType, tc.ctype)
			}
			if len(got.SHA256) != 64 || got.Size == 0 {
				t.Fatalf("digest/size = %q/%d", got.SHA256, got.Size)
			}
		})
	}
}

func TestScanRejectsRelative(t *testing.T) {
	if _, err := Scan("relative/dir"); err == nil {
		t.Fatal("relative dir accepted")
	}
}

func TestReadRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "public/favicon.svg", svgBytes)
	for _, rel := range []string{"../etc/passwd", "/etc/passwd", "public/../../x", "."} {
		if _, err := Read(root, rel); err == nil {
			t.Fatalf("Read(%q) succeeded", rel)
		}
	}
	if b, err := Read(root, "public/favicon.svg"); err != nil || !strings.HasPrefix(string(b), "<svg") {
		t.Fatalf("Read = %q, %v", b, err)
	}
}

func TestSniff(t *testing.T) {
	tests := map[string]string{
		string(pngBytes):                              "image/png",
		string(icoBytes):                              "image/x-icon",
		"\xff\xd8\xff\xe0rest":                        "image/jpeg",
		"RIFF\x00\x00\x00\x00WEBPVP8 ":                "image/webp",
		string(svgBytes):                              "image/svg+xml",
		"\xef\xbb\xbf  <?xml version=\"1.0\"?><svg/>": "image/svg+xml",
		"<html><svg/></html>":                         "",
		"GIF89a":                                      "",
		"":                                            "",
	}
	for in, want := range tests {
		if got := Sniff([]byte(in)); got != want {
			t.Errorf("Sniff(%q) = %q, want %q", in, got, want)
		}
	}
}
