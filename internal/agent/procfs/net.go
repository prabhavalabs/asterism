package procfs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net/netip"
)

// Socket is one row of /proc/net/{tcp,tcp6,udp,udp6}.
type Socket struct {
	Local  netip.Addr // IPv4-mapped IPv6 addresses are unmapped to IPv4
	Port   uint16
	Remote netip.Addr
	RPort  uint16
	State  uint8 // TCP state (0x0A = LISTEN); UDP unconnected = 0x07
	UID    uint32
	Inode  uint64
}

// TCP and UDP socket states used by the listener scan.
const (
	TCPListen = 0x0A
	UDPClose  = 0x07 // unconnected UDP sockets report TCP_CLOSE
)

// ParseSockets streams a /proc/net/{tcp,udp}[6] table and calls fn per row.
// It reads line by line so very large tables do not need one big buffer.
func ParseSockets(r io.Reader, fn func(Socket)) error {
	br := bufio.NewReaderSize(r, 16<<10)
	var f [12][]byte
	first := true
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 && !first {
			if s, ok := parseSocketLine(line, f[:0]); ok {
				fn(s)
			}
		}
		first = false
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if err == bufio.ErrBufferFull {
				// Overlong line: skip the rest of it.
				for err == bufio.ErrBufferFull {
					_, err = br.ReadSlice('\n')
				}
				if err == io.EOF {
					return nil
				}
				if err == nil {
					continue
				}
			}
			return err
		}
	}
}

// ParseSocketsBytes parses an in-memory table (fixtures, small files).
func ParseSocketsBytes(b []byte, fn func(Socket)) {
	_ = ParseSockets(bytes.NewReader(b), fn)
}

func parseSocketLine(line []byte, scratch [][]byte) (Socket, bool) {
	var s Socket
	fs := Fields(line, scratch)
	// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
	if len(fs) < 10 {
		return s, false
	}
	var ok bool
	if s.Local, s.Port, ok = parseHexAddr(fs[1]); !ok {
		return s, false
	}
	if s.Remote, s.RPort, ok = parseHexAddr(fs[2]); !ok {
		return s, false
	}
	st, ok := ParseHex(fs[3])
	if !ok {
		return s, false
	}
	s.State = uint8(st)
	uid, _ := ParseUint(fs[7])
	s.UID = uint32(uid)
	s.Inode, _ = ParseUint(fs[9])
	return s, true
}

// parseHexAddr decodes "0100007F:1F90" (IPv4) or a 32-hex-digit IPv6
// address. The kernel prints each 32-bit word in host byte order; every
// architecture Asterism ships for (amd64, arm64) is little-endian.
func parseHexAddr(b []byte) (netip.Addr, uint16, bool) {
	colon := bytes.IndexByte(b, ':')
	if colon < 0 {
		return netip.Addr{}, 0, false
	}
	port, ok := ParseHex(b[colon+1:])
	if !ok || port > 0xffff {
		return netip.Addr{}, 0, false
	}
	h := b[:colon]
	switch len(h) {
	case 8:
		w, ok := ParseHex(h)
		if !ok {
			return netip.Addr{}, 0, false
		}
		var a [4]byte
		binary.LittleEndian.PutUint32(a[:], uint32(w))
		return netip.AddrFrom4(a), uint16(port), true
	case 32:
		var a [16]byte
		for i := 0; i < 4; i++ {
			w, ok := ParseHex(h[i*8 : i*8+8])
			if !ok {
				return netip.Addr{}, 0, false
			}
			binary.LittleEndian.PutUint32(a[i*4:], uint32(w))
		}
		return netip.AddrFrom16(a).Unmap(), uint16(port), true
	}
	return netip.Addr{}, 0, false
}
