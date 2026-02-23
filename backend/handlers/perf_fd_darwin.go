//go:build darwin

package handlers

import "os"

func countOpenFDs() int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
