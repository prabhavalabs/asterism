// Package procfs reads Linux /proc, /sys and cgroup v2 files with long-lived
// file handles and allocation-free byte parsers.
//
// Collectors keep one File per hot path and re-read it with pread at offset
// 0 into a shared buffer; seq_file-backed files (procfs, sysfs, cgroupfs)
// regenerate their content on every read from offset 0. Parsers work on the
// returned bytes in place and never build strings on the hot path.
package procfs

import (
	"errors"
	"io"
	"os"
)

// BufSize is the default shared read buffer size (64 KiB).
const BufSize = 64 << 10

// ErrTruncated is returned when a file did not fit in the read buffer.
var ErrTruncated = errors.New("procfs: file larger than read buffer")

// File is a re-readable handle on a pseudo-file.
type File struct {
	path string
	f    *os.File
}

// Open opens path for repeated reads.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &File{path: path, f: f}, nil
}

// Path returns the opened path.
func (f *File) Path() string { return f.path }

// Read reads the whole file from offset 0 into buf and returns buf[:n]. It
// returns ErrTruncated (with the partial bytes) when the content fills buf.
func (f *File) Read(buf []byte) ([]byte, error) {
	if f == nil || f.f == nil {
		return nil, os.ErrClosed
	}
	n, err := f.f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == len(buf) && err == nil {
		return buf[:n], ErrTruncated
	}
	return buf[:n], nil
}

// Close closes the handle. It is safe to call more than once.
func (f *File) Close() error {
	if f == nil || f.f == nil {
		return nil
	}
	err := f.f.Close()
	f.f = nil
	return err
}

// ReadFile reads a small file into buf (one-shot, no long-lived handle).
func ReadFile(path string, buf []byte) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	switch {
	case err == nil:
		return buf[:n], ErrTruncated
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return buf[:n], nil
	default:
		return nil, err
	}
}
