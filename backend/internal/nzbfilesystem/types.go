package nzbfilesystem

import (
	pathpkg "path"
)

// normalizePath normalizes file paths for consistent database lookups.
// The path is cleaned as an absolute slash path (collapsing "." and ".."
// segments and trailing slashes); the root path "/" is kept as is.
func normalizePath(path string) string {
	// Handle empty path
	if path == "" {
		return RootPath
	}

	return pathpkg.Clean("/" + path)
}
