// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// The Linux path (NFR-018: Linux is the primary operating scope).
//
// statfs(2) answers with a numeric magic, not a name: the kernel does not
// tell userspace "vfat", it tells it 0x4d44. The mapping below is the
// stable half of linux/magic.h — those constants are part of the kernel's
// userspace contract and do not change. A magic absent from the table is
// reported unidentified WITH its hexadecimal value, so an operator on a
// filesystem this build has not heard of can say which one it was.
//
// /proc/mounts is deliberately not consulted. It names filesystems in
// words, which would be easier to read, but it describes the mount
// namespace of the process that reads it — inside a container it can
// perfectly well not contain the store's own mount — and a lookup that
// silently found nothing there would report "unidentified" for a volume
// statfs identifies correctly.
//
// The table lists more names than typeLimits knows ceilings for. That is
// the point: naming tmpfs, overlay or nfs and stopping there is a more
// useful report than "0x794c7630", and none of them is thereby declared
// capable — the ceiling lookup in classify is what decides that.
var linuxMagics = map[int64]string{
	0x4d44:     "vfat",  // MSDOS_SUPER_MAGIC — FAT12/16/32
	0x2011BAB0: "exfat", // EXFAT_SUPER_MAGIC
	0x5346544e: "ntfs",  // NTFS_SB_MAGIC (ntfs / ntfs-3g)
	0x7366746e: "ntfs3", // NTFS3_SUPER_MAGIC
	0xEF53:     "ext4",  // EXT2/3/4_SUPER_MAGIC — one magic for the family
	0x58465342: "xfs",   // XFS_SUPER_MAGIC
	0x9123683E: "btrfs", // BTRFS_SUPER_MAGIC
	0xF2F52010: "f2fs",  // F2FS_SUPER_MAGIC
	0x2FC12FC1: "zfs",   // ZFS_SUPER_MAGIC
	0xCA451A4E: "bcachefs",
	0x01021994: "tmpfs",     // TMPFS_MAGIC — ceiling is the mount's own size
	0x858458F6: "ramfs",     // RAMFS_MAGIC — same
	0x794c7630: "overlayfs", // OVERLAYFS_SUPER_MAGIC — ceiling is the upper layer's
	0x6969:     "nfs",       // NFS_SUPER_MAGIC — ceiling is the server's
	0xFF534D42: "cifs",      // CIFS_SUPER_MAGIC — same
	0x00C36400: "ceph",      // CEPH_SUPER_MAGIC
	0x65735546: "fuse",      // FUSE_SUPER_MAGIC — ceiling is the driver's
	0x65735543: "fuseblk",
	0x73717368: "squashfs", // read-only
	0x9660:     "iso9660",  // read-only
	0x01021997: "v9fs",
}

// systemInspector reads the volume through statfs(2).
type systemInspector struct{}

func (systemInspector) Inspect(path string) (Filesystem, Space, error) {
	target, err := nearestExisting(path)
	if err != nil {
		return Filesystem{}, Space{}, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(target, &st); err != nil {
		return Filesystem{}, Space{}, fmt.Errorf("statfs %s: %w", target, err)
	}

	// st.Type and st.Bsize are int64 on every architecture this project
	// releases for (linux/amd64 and linux/arm64). They are int32 on 32-bit
	// Linux, where these two lines would stop compiling — which is the
	// right way to find out, rather than a conversion that is dead on
	// every build we actually ship and that `unconvert` flags as such.
	magic := st.Type
	name, known := linuxMagics[magic]
	f := classify(name, "statfs(2) f_type")
	if !known {
		f.Type = fmt.Sprintf("0x%x", magic)
		f.Identified = false
		f.Note = "statfs reported a filesystem magic this build does not know"
	}

	// f_bavail, not f_bfree: the blocks an ext filesystem reserves for
	// root are free to the kernel and unavailable to the instance, and
	// counting them would promise space a non-root process cannot have.
	blockSize := st.Bsize
	space := Space{
		FreeBytes:  int64(st.Bavail) * blockSize, //nolint:gosec // G115: block counts of a real volume are orders of magnitude below the int64 range
		TotalBytes: int64(st.Blocks) * blockSize, //nolint:gosec // G115: same
		Known:      true,
	}
	return f, space, nil
}
