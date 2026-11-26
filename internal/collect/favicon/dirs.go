package favicon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/iconscan"
)

// DirRef is a favicon candidate found in a project's working directory.
type DirRef struct {
	WorkingDir string
	RelPath    string
	SHA256     string
	Size       int
	// Generated marks showcase icons (no file behind them).
	Generated bool
}

// LocalDirs scans compose working directories on the hub's own filesystem
// (collect.mode = "local"). Results are cached per directory so the scan
// runs at most once per TTL.
type LocalDirs struct {
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]localEntry
	now   func() time.Time
}

type localEntry struct {
	ref DirRef
	ok  bool
	at  time.Time
}

// NewLocalDirs returns a scanner with a 30 minute cache.
func NewLocalDirs() *LocalDirs {
	return &LocalDirs{TTL: 30 * time.Minute, cache: map[string]localEntry{}, now: time.Now}
}

// DirIcon returns the best icon under workingDir.
func (l *LocalDirs) DirIcon(workingDir string) (DirRef, bool) {
	l.mu.Lock()
	e, hit := l.cache[workingDir]
	l.mu.Unlock()
	if hit && l.now().Sub(e.at) < l.TTL {
		return e.ref, e.ok
	}
	c, err := iconscan.Scan(workingDir)
	e = localEntry{at: l.now(), ok: err == nil}
	if err == nil {
		e.ref = DirRef{WorkingDir: workingDir, RelPath: c.RelPath, SHA256: c.SHA256, Size: int(c.Size)}
	}
	l.mu.Lock()
	if len(l.cache) > 512 {
		clear(l.cache)
	}
	l.cache[workingDir] = e
	l.mu.Unlock()
	return e.ref, e.ok
}

// ReadDirIcon reads the file again and checks it still matches the digest.
func (l *LocalDirs) ReadDirIcon(_ context.Context, ref DirRef) ([]byte, error) {
	data, err := iconscan.Read(ref.WorkingDir, ref.RelPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		l.mu.Lock()
		delete(l.cache, ref.WorkingDir)
		l.mu.Unlock()
		return nil, errors.New("icon file changed since it was scanned")
	}
	return data, nil
}
