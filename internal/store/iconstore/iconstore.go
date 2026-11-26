// Package iconstore keeps project icon images under <data_dir>/icons/:
// uploaded/ for admin uploads and favicon/ for the discovery cache.
//
// Each icon is two files named by a hash of the project name (never the
// name itself): <key>.img with the validated bytes and <key>.json with its
// metadata. Both are written atomically (temp file + rename) with mode 0600.
// A favicon entry may have metadata without an image: that is the negative
// cache of a failed discovery.
package iconstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Slot is a storage area.
type Slot string

// Slots.
const (
	SlotUploaded Slot = "uploaded"
	SlotFavicon  Slot = "favicon"
)

var slots = []Slot{SlotUploaded, SlotFavicon}

// Meta describes one stored icon.
type Meta struct {
	Project     string `json:"project"`
	ContentType string `json:"content_type,omitempty"`
	// SHA256 is the hex digest of the image; empty for a negative entry.
	SHA256 string `json:"sha256,omitempty"`
	Size   int    `json:"size,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Source is "upload", "directory", "domain" or "generated".
	Source string `json:"source,omitempty"`
	// Origin is the relative file path or https URL of a favicon.
	Origin    string `json:"origin,omitempty"`
	UpdatedMs int64  `json:"updated_ms,omitempty"`
	// CheckedMs and Status record the last favicon discovery attempt.
	CheckedMs int64  `json:"checked_ms,omitempty"`
	Status    string `json:"status,omitempty"`
	// Actor uploaded the image.
	Actor string `json:"actor,omitempty"`
}

// HasImage reports whether the entry carries image bytes.
func (m Meta) HasImage() bool { return m.SHA256 != "" }

// ETag is the strong validator served for the image.
func (m Meta) ETag() string {
	if len(m.SHA256) < 16 {
		return ""
	}
	return m.SHA256[:16]
}

// Store is safe for concurrent use.
type Store struct {
	root string
	mu   sync.RWMutex
	idx  map[Slot]map[string]Meta // slot -> key -> meta
}

// Key returns the file-name key of a project: 32 hex characters of a
// SHA-256 over the name, so names are never used as paths.
func Key(project string) string {
	sum := sha256.Sum256([]byte("asterism-project-icon\x00" + project))
	return hex.EncodeToString(sum[:16])
}

// ValidKey reports whether k has the shape Key returns.
func ValidKey(k string) bool {
	if len(k) != 32 {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil && strings.ToLower(k) == k
}

// Open creates <dataDir>/icons/{uploaded,favicon} (mode 0700) and loads the
// metadata index.
func Open(dataDir string) (*Store, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("iconstore: data dir %q must be absolute", dataDir)
	}
	s := &Store{root: filepath.Join(dataDir, "icons"), idx: map[Slot]map[string]Meta{}}
	for _, sl := range slots {
		dir := filepath.Join(s.root, string(sl))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("iconstore: %w", err)
		}
		s.idx[sl] = map[string]Meta{}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("iconstore: %w", err)
		}
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".tmp-") {
				_ = os.Remove(filepath.Join(dir, name)) // interrupted write
				continue
			}
			key, ok := strings.CutSuffix(name, ".json")
			if !ok || !ValidKey(key) || !e.Type().IsRegular() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var m Meta
			if json.Unmarshal(raw, &m) != nil || Key(m.Project) != key {
				continue
			}
			if m.HasImage() {
				if st, err := os.Lstat(s.imgPath(sl, key)); err != nil || !st.Mode().IsRegular() {
					m.SHA256 = "" // image missing: keep only the check record
				}
			}
			s.idx[sl][key] = m
		}
	}
	return s, nil
}

func (s *Store) imgPath(sl Slot, key string) string {
	return filepath.Join(s.root, string(sl), key+".img")
}

func (s *Store) metaPath(sl Slot, key string) string {
	return filepath.Join(s.root, string(sl), key+".json")
}

// Get returns the entry of project in slot.
func (s *Store) Get(sl Slot, project string) (Meta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.idx[sl][Key(project)]
	return m, ok
}

// Put stores data (already validated) and meta for project in slot. meta's
// Project, SHA256 and Size are filled in.
func (s *Store) Put(sl Slot, project string, data []byte, meta Meta) (Meta, error) {
	if len(data) == 0 {
		return Meta{}, errors.New("iconstore: empty image")
	}
	key := Key(project)
	sum := sha256.Sum256(data)
	meta.Project = project
	meta.SHA256 = hex.EncodeToString(sum[:])
	meta.Size = len(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(s.imgPath(sl, key), data); err != nil {
		return Meta{}, err
	}
	if err := s.writeMetaLocked(sl, key, meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Update rewrites only the metadata of an entry (for example a negative
// favicon check that keeps the previous image).
func (s *Store) Update(sl Slot, project string, fn func(m *Meta)) (Meta, error) {
	key := Key(project)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.idx[sl][key]
	m.Project = project
	fn(&m)
	m.Project = project
	if err := s.writeMetaLocked(sl, key, m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

func (s *Store) writeMetaLocked(sl Slot, key string, m Meta) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.metaPath(sl, key), raw); err != nil {
		return err
	}
	s.idx[sl][key] = m
	return nil
}

// Delete removes the entry of project in slot (no error when absent).
func (s *Store) Delete(sl Slot, project string) error {
	key := Key(project)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.idx[sl], key)
	var errs []error
	for _, p := range []string{s.metaPath(sl, key), s.imgPath(sl, key)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Read returns the image bytes of key in slot, verifying them against the
// recorded digest.
func (s *Store) Read(sl Slot, key string) ([]byte, Meta, error) {
	if !ValidKey(key) {
		return nil, Meta{}, os.ErrNotExist
	}
	s.mu.RLock()
	m, ok := s.idx[sl][key]
	s.mu.RUnlock()
	if !ok || !m.HasImage() {
		return nil, Meta{}, os.ErrNotExist
	}
	data, err := os.ReadFile(s.imgPath(sl, key))
	if err != nil {
		return nil, Meta{}, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return nil, Meta{}, fmt.Errorf("iconstore: %s/%s does not match its digest", sl, key)
	}
	return data, m, nil
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("iconstore: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("iconstore: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("iconstore: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("iconstore: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("iconstore: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("iconstore: %w", err)
	}
	ok = true
	return nil
}
