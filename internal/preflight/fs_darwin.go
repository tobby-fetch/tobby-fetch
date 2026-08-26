// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// The macOS path. darwin is the convenience tier (NFR-001), but its
// statfs is the friendliest of the three: f_fstypename carries the
// filesystem's name directly — "apfs", "hfs", "msdos", "exfat", "ntfs",
// "smbfs" — so no magic-number table is needed and a FAT volume mounted
// from a USB stick identifies itself in one call.
//
// The names are the ones mount(8) prints, which is what typeLimits is
// keyed on; anything else falls through to unidentified, warned, and
// allowed to proceed.

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

	name := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, c)
	}
	f := classify(string(name), "statfs(2) f_fstypename")

	// f_bavail, not f_bfree: the reserve is the kernel's, not the
	// instance's.
	blockSize := int64(st.Bsize)
	space := Space{
		FreeBytes:  int64(st.Bavail) * blockSize, //nolint:gosec // G115: block counts of a real volume are orders of magnitude below the int64 range
		TotalBytes: int64(st.Blocks) * blockSize, //nolint:gosec // G115: same
		Known:      true,
	}
	return f, space, nil
}
