//go:build linux || darwin

package storageguard

import "golang.org/x/sys/unix"

func available(path string) (int64, error) {
	var info unix.Statfs_t
	if err := unix.Statfs(path, &info); err != nil {
		return 0, err
	}
	return int64(info.Bavail) * int64(info.Bsize), nil
}
