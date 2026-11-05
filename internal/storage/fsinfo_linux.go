//go:build linux

package storage

import "syscall"

// fsKind reads the filesystem identity a Linux mount reports. The magic is the fact; the name is only
// for the person reading the table. A mount this table has never heard of is still classified by what
// it does, not by what it is called.
func fsKind(dir string) (magic uint64, name string, network bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, "unknown", false
	}
	m := uint64(st.Type)
	switch m {
	case 0xef53:
		return m, "ext4", false
	case 0x58465342:
		return m, "xfs", false
	case 0x9123683e:
		return m, "btrfs", false
	case 0x01021994:
		return m, "tmpfs", false
	case 0x794c7630:
		return m, "overlay", false
	case 0x6969:
		return m, "nfs", true
	case 0xff534d42:
		return m, "cifs", true
	case 0xfe534d42:
		return m, "smb2", true
	case 0x0bd00bd0:
		return m, "lustre", true
	case 0x00c36400:
		return m, "ceph", true
	case 0x5346414f:
		return m, "afs", true
	case 0x01021997:
		return m, "9p", true
	case 0x65735546:
		// FUSE is either a local mount (gcsfuse-style caches) or a network one (sshfs). Treating it as
		// shared is the conservative direction: the checks then have to pass before we promise anything.
		return m, "fuse", true
	default:
		return m, "other", false
	}
}
