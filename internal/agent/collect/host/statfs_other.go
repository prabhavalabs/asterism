//go:build !linux && !darwin

package host

import "errors"

func statfs(string) (total, used, avail uint64, err error) {
	return 0, 0, 0, errors.New("statfs is not supported on this platform")
}
