// Package favicon validates project icon images and discovers favicons on a
// project's public hostnames.
package favicon

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for JPEG
	_ "image/png"  // DecodeConfig for PNG
	"io"
	"strings"

	"github.com/prabhavalabs/asterism/internal/iconscan"
)

// Size limits.
const (
	// MaxUploadBytes caps an admin-uploaded icon.
	MaxUploadBytes = 512 << 10
	// MaxFaviconBytes caps a discovered favicon (directory or domain).
	MaxFaviconBytes = iconscan.MaxBytes
	// maxDimension caps raster width and height.
	maxDimension = 4096
)

// Image is a validated icon.
type Image struct {
	ContentType string
	// Width and Height are 0 for SVG.
	Width, Height int
}

// ErrNotImage means the bytes are not an accepted image.
var ErrNotImage = errors.New("not a PNG, SVG, ICO, WebP or JPEG image")

// Validate sniffs data, checks it decodes as the sniffed type, and bounds
// its size and dimensions. declared (optional) must agree with the sniffed
// type when set.
func Validate(data []byte, declared string, maxBytes int) (Image, error) {
	if len(data) == 0 {
		return Image{}, errors.New("empty image")
	}
	if len(data) > maxBytes {
		return Image{}, fmt.Errorf("image is %d bytes; the limit is %d KiB", len(data), maxBytes>>10)
	}
	ct := iconscan.Sniff(data)
	if ct == "" {
		return Image{}, ErrNotImage
	}
	if d := normalizeType(declared); d != "" && d != "application/octet-stream" && d != ct {
		return Image{}, fmt.Errorf("declared type %q does not match the content (%s)", declared, ct)
	}
	img := Image{ContentType: ct}
	var err error
	switch ct {
	case "image/png", "image/jpeg":
		var cfg image.Config
		cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
		img.Width, img.Height = cfg.Width, cfg.Height
	case "image/x-icon":
		img.Width, img.Height, err = icoSize(data)
	case "image/webp":
		img.Width, img.Height, err = webpSize(data)
	case "image/svg+xml":
		err = checkSVG(data)
	}
	if err != nil {
		return Image{}, fmt.Errorf("invalid %s: %w", ct, err)
	}
	if ct != "image/svg+xml" && (img.Width <= 0 || img.Height <= 0 || img.Width > maxDimension || img.Height > maxDimension) {
		return Image{}, fmt.Errorf("image is %dx%d; dimensions must be 1-%d px", img.Width, img.Height, maxDimension)
	}
	return img, nil
}

// normalizeType maps common aliases to the canonical sniffed names.
func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "image/vnd.microsoft.icon", "image/ico", "image/icon":
		return "image/x-icon"
	case "image/jpg", "image/pjpeg":
		return "image/jpeg"
	case "image/svg":
		return "image/svg+xml"
	}
	return t
}

// icoSize reads the first directory entry of an ICO file (0 means 256).
func icoSize(data []byte) (int, int, error) {
	if len(data) < 6+16 {
		return 0, 0, errors.New("truncated header")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 || count > 64 || len(data) < 6+16*count {
		return 0, 0, errors.New("bad image count")
	}
	best := 0
	bw, bh := 0, 0
	for i := range count {
		e := data[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := binary.LittleEndian.Uint32(e[8:12])
		off := binary.LittleEndian.Uint32(e[12:16])
		if uint64(off)+uint64(size) > uint64(len(data)) || size == 0 {
			return 0, 0, errors.New("image data outside the file")
		}
		if w*h > best {
			best, bw, bh = w*h, w, h
		}
	}
	return bw, bh, nil
}

// webpSize reads the canvas size from a VP8, VP8L or VP8X chunk.
func webpSize(data []byte) (int, int, error) {
	if len(data) < 30 {
		return 0, 0, errors.New("truncated header")
	}
	chunk := string(data[12:16])
	p := data[20:]
	switch chunk {
	case "VP8 ":
		if p[3] != 0x9d || p[4] != 0x01 || p[5] != 0x2a {
			return 0, 0, errors.New("bad VP8 signature")
		}
		w := int(binary.LittleEndian.Uint16(p[6:8]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(p[8:10]) & 0x3fff)
		return w, h, nil
	case "VP8L":
		if p[0] != 0x2f {
			return 0, 0, errors.New("bad VP8L signature")
		}
		b := binary.LittleEndian.Uint32(p[1:5])
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(uint32(p[4])|uint32(p[5])<<8|uint32(p[6])<<16) + 1
		h := int(uint32(p[7])|uint32(p[8])<<8|uint32(p[9])<<16) + 1
		return w, h, nil
	}
	return 0, 0, fmt.Errorf("unknown chunk %q", chunk)
}

// checkSVG requires well-formed XML whose root element is <svg> and refuses
// DTDs (entity expansion). Scripts cannot run: icons are rendered with <img>
// and served with a sandboxing Content-Security-Policy.
func checkSVG(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	root := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return errors.New("DTDs and entity declarations are not allowed")
		case xml.StartElement:
			if root == "" {
				root = t.Name.Local
				if root != "svg" {
					return fmt.Errorf("root element is <%s>, not <svg>", root)
				}
			}
		}
	}
	if root == "" {
		return errors.New("no <svg> element")
	}
	return nil
}
