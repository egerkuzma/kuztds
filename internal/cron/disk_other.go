//go:build !unix

package cron

import "errors"

func diskFree(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk monitoring is not supported on this platform")
}
