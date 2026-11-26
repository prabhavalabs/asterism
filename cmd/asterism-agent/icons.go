package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/iconscan"
)

// Icon scan limits: the scan is cheap (a few dozen stat calls per project)
// and runs rarely; reads for the hub are rate limited.
const (
	iconScanEvery   = 15 * time.Minute
	iconFirstScan   = 30 * time.Second
	iconMaxDirs     = 64
	iconReadsPerMin = 20
)

// iconScanner finds favicon files in compose working directories and
// answers the hub's IconRequests for them.
type iconScanner struct {
	cfg   *config.Config
	store *state.Store
	log   *slog.Logger

	mu        sync.Mutex
	window    time.Time
	readsLeft int
}

func newIconScanner(cfg *config.Config, store *state.Store, log *slog.Logger) *iconScanner {
	return &iconScanner{cfg: cfg, store: store, log: log}
}

func (s *iconScanner) run(ctx context.Context) {
	t := time.NewTimer(iconFirstScan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.scan()
		t.Reset(iconScanEvery)
	}
}

// scan rescans every distinct compose working directory.
func (s *iconScanner) scan() {
	dirs := map[string]bool{}
	for _, c := range s.store.Containers() {
		if wd := c.GetComposeWorkingDir(); wd != "" {
			dirs[wd] = true
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Strings(list)
	if len(list) > iconMaxDirs {
		list = list[:iconMaxDirs]
	}
	refs := make([]*v1.ProjectIconRef, 0, len(list))
	for _, wd := range list {
		c, err := iconscan.Scan(s.cfg.HostPath(wd))
		if err != nil {
			if !errors.Is(err, iconscan.ErrNotFound) {
				s.log.Debug("project icon scan skipped a directory", "dir", wd, "err", err)
			}
			continue
		}
		refs = append(refs, &v1.ProjectIconRef{WorkingDir: wd, RelPath: c.RelPath, Sha256: c.SHA256, SizeBytes: uint32(c.Size)})
	}
	s.store.ReplaceProjectIcons(refs)
}

// ReadIcon returns the bytes of a reported icon, re-checking its digest.
func (s *iconScanner) ReadIcon(digest string) ([]byte, error) {
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.window) >= time.Minute {
		s.window, s.readsLeft = now, iconReadsPerMin
	}
	if s.readsLeft <= 0 {
		s.mu.Unlock()
		return nil, errors.New("icon read rate limit reached; try again in a minute")
	}
	s.readsLeft--
	s.mu.Unlock()

	ref, ok := s.store.ProjectIcon(digest)
	if !ok {
		return nil, errors.New("unknown icon digest")
	}
	data, err := iconscan.Read(s.cfg.HostPath(ref.GetWorkingDir()), ref.GetRelPath())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("icon file changed since it was scanned")
	}
	return data, nil
}
