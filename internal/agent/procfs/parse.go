package procfs

import "bytes"

// NextLine splits b at the first newline.
func NextLine(b []byte) (line, rest []byte) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

// Fields appends the whitespace-separated fields of line to out[:0] and
// returns it. Reusing out across calls keeps parsing allocation-free.
func Fields(line []byte, out [][]byte) [][]byte {
	out = out[:0]
	i := 0
	for i < len(line) {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			break
		}
		j := i
		for j < len(line) && !isSpace(line[j]) {
			j++
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

// Field returns the n-th (0-based) whitespace-separated field of line.
func Field(line []byte, n int) []byte {
	i := 0
	for k := 0; ; k++ {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return nil
		}
		j := i
		for j < len(line) && !isSpace(line[j]) {
			j++
		}
		if k == n {
			return line[i:j]
		}
		i = j
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// ParseUint parses a decimal unsigned integer.
func ParseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if v > (^uint64(0)-d)/10 {
			return 0, false
		}
		v = v*10 + d
	}
	return v, true
}

// ParseInt parses a decimal integer with optional sign.
func ParseInt(b []byte) (int64, bool) {
	neg := false
	if len(b) > 0 && (b[0] == '-' || b[0] == '+') {
		neg = b[0] == '-'
		b = b[1:]
	}
	u, ok := ParseUint(b)
	if !ok || u > 1<<63 {
		return 0, false
	}
	if neg {
		return -int64(u), true
	}
	return int64(u), true
}

// ParseHex parses a hexadecimal unsigned integer (no prefix).
func ParseHex(b []byte) (uint64, bool) {
	if len(b) == 0 || len(b) > 16 {
		return 0, false
	}
	var v uint64
	for _, c := range b {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | uint64(d)
	}
	return v, true
}

// ParseDecimal parses a non-negative decimal such as "0.52" or "12345.67"
// without allocating.
func ParseDecimal(b []byte) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var ip, fp, scale float64 = 0, 0, 1
	seenDot, any := false, false
	for _, c := range b {
		switch {
		case c == '.' && !seenDot:
			seenDot = true
		case c >= '0' && c <= '9':
			any = true
			if seenDot {
				scale *= 10
				fp = fp*10 + float64(c-'0')
			} else {
				ip = ip*10 + float64(c-'0')
			}
		default:
			return 0, false
		}
	}
	if !any {
		return 0, false
	}
	return ip + fp/scale, true
}

// KeyValue scans "key value" lines (cpu.stat, memory.stat, memory.events,
// /proc/meminfo with "Key:" form) and returns the first value of key.
func KeyValue(b, key []byte) (uint64, bool) {
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		if len(line) <= len(key) || !bytes.HasPrefix(line, key) {
			continue
		}
		c := line[len(key)]
		if c != ' ' && c != '\t' && c != ':' {
			continue
		}
		return ParseUint(Field(line[len(key)+1:], 0))
	}
	return 0, false
}

// Single parses a single-value file such as memory.current, pids.current or
// memory.max. "max" reports ok=false.
func Single(b []byte) (uint64, bool) {
	return ParseUint(bytes.TrimSpace(b))
}
