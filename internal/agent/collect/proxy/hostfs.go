package proxy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
)

// HostFS reads host files by absolute host path under a root prefix. It
// resolves symlinks itself so an absolute link such as
// sites-enabled/shop -> /etc/nginx/sites-available/shop stays inside the
// root when the agent runs in a container with the host's /etc mounted at
// /host/etc. Files are capped at ingress.MaxFileBytes.
type HostFS struct {
	Root string // "/" or "/host"
}

var _ ingress.Reader = HostFS{}

const maxHops = 40

func (h HostFS) root() string {
	if h.Root == "" {
		return "/"
	}
	return h.Root
}

// Resolve maps an absolute host path to a path on the agent's filesystem,
// following symlinks within the root.
func (h HostFS) Resolve(p string) (string, error) {
	if !path.IsAbs(p) {
		return "", fmt.Errorf("not an absolute path: %s", p)
	}
	root := h.root()
	if root == "/" {
		return path.Clean(p), nil
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(p), "/"), "/")
	cur := "/"
	hops := 0
	for i := 0; i < len(parts); i++ {
		next := path.Join(cur, parts[i])
		real := filepath.Join(root, next)
		fi, err := os.Lstat(real)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxHops {
				return "", errors.New("too many symlinks")
			}
			target, err := os.Readlink(real)
			if err != nil {
				return "", err
			}
			if !path.IsAbs(target) {
				target = path.Join(cur, target)
			}
			rest := append(strings.Split(strings.TrimPrefix(path.Clean(target), "/"), "/"), parts[i+1:]...)
			parts, cur, i = rest, "/", -1
			continue
		}
		cur = next
	}
	return filepath.Join(root, cur), nil
}

// ReadFile implements ingress.Reader.
func (h HostFS) ReadFile(name string) ([]byte, error) {
	real, err := h.Resolve(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, ingress.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > ingress.MaxFileBytes {
		return nil, ingress.ErrTooLarge
	}
	return b, nil
}

// Glob implements ingress.Reader (the directory part is resolved; the
// pattern applies to its entries).
func (h HostFS) Glob(pattern string) ([]string, error) {
	dir, pat := path.Split(pattern)
	if strings.ContainsAny(dir, "*?[") {
		return nil, fmt.Errorf("glob in directory part is not supported: %s", pattern)
	}
	real, err := h.Resolve(path.Clean(dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, nil
	}
	var out []string
	for _, e := range ents {
		if ok, _ := path.Match(pat, e.Name()); ok {
			p := path.Join(dir, e.Name())
			if r, err := h.Resolve(p); err == nil {
				if st, err := os.Stat(r); err == nil && !st.IsDir() {
					out = append(out, p)
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Stat returns size and mtime for change detection.
func (h HostFS) Stat(name string) (fs.FileInfo, error) {
	real, err := h.Resolve(name)
	if err != nil {
		return nil, err
	}
	return os.Stat(real)
}
