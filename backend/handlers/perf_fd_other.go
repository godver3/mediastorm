//go:build !linux && !darwin

package handlers

func countOpenFDs() int {
	return -1
}
