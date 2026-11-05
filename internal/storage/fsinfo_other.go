//go:build !linux

package storage

// fsKind cannot tell a local from a shared mount on this platform. The checks still run; the tier is
// then decided by behaviour alone, and the table says the mount type was not identified.
func fsKind(dir string) (magic uint64, name string, network bool) {
	return 0, "unknown", false
}
