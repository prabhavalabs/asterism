// Package iconscan finds a project's favicon or logo inside its compose
// working directory.
//
// The scan is deliberately small and predictable: it looks only in a fixed
// list of conventional web-asset folders (plus one level of monorepo
// "apps/*" and "packages/*" folders), never follows symbolic links, reads at
// most one file, and caps every read at MaxBytes. It uses the standard
// library only, so asterism-agent can link it.
package iconscan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxBytes is the largest icon file the scanner reads.
const MaxBytes = 256 << 10

// Candidate is the chosen icon file of one directory.
type Candidate struct {
	// RelPath is slash-separated and relative to the scanned directory.
	RelPath string
	Size    int64
	// SHA256 is the lowercase hex digest of the file.
	SHA256      string
	ContentType string
}

// baseDirs are searched in order of preference.
var baseDirs = []string{
	"public", "static", "web/public", "frontend/public", "client/public", "app/public",
	"src/app", "app", "assets", "src/assets", "static/img", "static/images",
	"public/img", "public/images", "public/static", "web/static", "www", "html", ".",
}

// monorepoRoots get one level of children expanded into <root>/<child>/public
// and <root>/<child>/src/app.
var monorepoRoots = []string{"apps", "packages"}

// fileNames are the accepted file names, best first.
var fileNames = []string{
	"favicon.svg", "icon.svg", "apple-touch-icon.png", "apple-icon.png",
	"android-chrome-512x512.png", "android-chrome-192x192.png", "icon-512.png", "icon-192.png",
	"icon.png", "favicon.png", "favicon-96x96.png", "favicon-32x32.png", "favicon.webp",
	"favicon.ico", "logo.svg", "logo.png", "logo.webp", "logo.jpg", "logo.jpeg",
}

// Limits bound the work of one scan.
const (
	maxDirs          = 48
	maxMonorepoChild = 24
)

// ErrNotFound means no candidate file exists.
var ErrNotFound = errors.New("no favicon or logo found")

// Dirs returns the directories (slash-separated, relative to the project
// root) the scanner looks in, in order of preference. The monorepo roots are
// expanded with readDir.
func Dirs(readDir func(rel string) []string) []string {
	out := make([]string, 0, maxDirs)
	out = append(out, baseDirs...)
	for _, root := range monorepoRoots {
		children := readDir(root)
		sort.Strings(children)
		if len(children) > maxMonorepoChild {
			children = children[:maxMonorepoChild]
		}
		for _, c := range children {
			out = append(out, root+"/"+c+"/public", root+"/"+c+"/src/app")
		}
	}
	if len(out) > maxDirs {
		out = out[:maxDirs]
	}
	return out
}

// Rank orders candidate names: lower is better; -1 means not accepted.
func Rank(name string) int {
	lower := strings.ToLower(name)
	for i, n := range fileNames {
		if lower == n {
			return i
		}
	}
	return -1
}

// Scan returns the best icon file under dir.
func Scan(dir string) (Candidate, error) {
	if !filepath.IsAbs(dir) {
		return Candidate{}, fmt.Errorf("iconscan: %q is not an absolute path", dir)
	}
	root := filepath.Clean(dir)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", root)
		}
		return Candidate{}, err
	}
	readDir := func(rel string) []string {
		if !safeDir(root, rel) {
			return nil
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil
		}
		defer f.Close()
		ents, _ := f.ReadDir(maxMonorepoChild * 4)
		var out []string
		for _, e := range ents {
			if e.Type().IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" {
				out = append(out, e.Name())
			}
		}
		return out
	}

	type hit struct {
		rel        string
		name, dirI int
		size       int64
	}
	var best *hit
	for di, rel := range Dirs(readDir) {
		if best != nil && best.name == 0 {
			break
		}
		if !safeDir(root, rel) {
			continue
		}
		for ni, name := range fileNames {
			if best != nil && (ni > best.name || (ni == best.name && di >= best.dirI)) {
				break
			}
			p := path.Join(rel, name)
			st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > MaxBytes {
				continue
			}
			best = &hit{rel: p, name: ni, dirI: di, size: st.Size()}
			break
		}
	}
	if best == nil {
		return Candidate{}, ErrNotFound
	}
	data, err := Read(root, best.rel)
	if err != nil {
		return Candidate{}, err
	}
	ct := Sniff(data)
	if ct == "" {
		return Candidate{}, fmt.Errorf("%s is not a recognised image", best.rel)
	}
	sum := sha256.Sum256(data)
	return Candidate{RelPath: best.rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), ContentType: ct}, nil
}

// safeDir reports whether every component of rel under root is a real
// directory (no symlinks, no "..").
func safeDir(root, rel string) bool {
	if rel == "." || rel == "" {
		return true
	}
	if path.IsAbs(rel) || strings.Contains(rel, "..") {
		return false
	}
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil || !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// Read returns the bytes of rel under root after re-checking that no path
// component is a symlink and the file is a regular file of at most MaxBytes.
func Read(root, rel string) ([]byte, error) {
	rel = path.Clean(rel)
	if rel == "." || path.IsAbs(rel) || strings.HasPrefix(rel, "../") || rel == ".." {
		return nil, fmt.Errorf("invalid icon path %q", rel)
	}
	if !safeDir(root, path.Dir(rel)) {
		return nil, fmt.Errorf("icon path %q crosses a symlink or a missing directory", rel)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if st.Size() > MaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", rel, MaxBytes)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", rel, MaxBytes)
	}
	return data, nil
}

// Sniff returns the image content type of data from its magic bytes, or ""
// when data is not one of the accepted formats (PNG, ICO, JPEG, WebP, SVG).
func Sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 && (data[4] != 0 || data[5] != 0):
		return "image/x-icon"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	head = bytes.TrimSpace(head)
	lower := bytes.ToLower(head)
	if bytes.HasPrefix(lower, []byte("<svg")) || (bytes.HasPrefix(lower, []byte("<?xml")) || bytes.HasPrefix(lower, []byte("<!--"))) && bytes.Contains(lower, []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}
