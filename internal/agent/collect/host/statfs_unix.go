//go:build linux || darwin

package host

import "syscall"

// statfs returns total and used bytes (used = total - free, as df does with
// root-reserved blocks counted as used).
func statfs(p string) (total, used, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize)
	total = uint64(st.Blocks) * bs
	free := uint64(st.Bfree) * bs
	avail = uint64(st.Bavail) * bs
	if free > total {
		free = total
	}
	return total, total - free, avail, nil
}
