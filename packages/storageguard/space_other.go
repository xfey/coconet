//go:build !linux && !darwin

package storageguard

import "errors"

func available(string) (int64, error) {
	return 0, errors.New("server capacity protection requires Linux or macOS")
}
