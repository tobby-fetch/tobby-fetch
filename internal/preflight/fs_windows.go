// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// The Windows path. NFR-018 puts Windows in the operating scope of the
// mirror journey, so this is not an optional branch: the physical medium
// of an air-gapped transfer is very often prepared on a Windows
// workstation, and FAT32 is exactly what a USB stick formatted there
// carries.
//
// Two calls answer the question, and both are documented Win32 API, not
// heuristics:
//
//   - GetVolumePathNameW maps an arbitrary path onto the mount point of
//     the volume holding it. It is needed because the other two calls
//     want a volume root, and a store at D:\tobby\store is not one. It
//     also resolves mounted folders — a volume mounted at C:\media\usb
//     with no drive letter answers correctly, where naive drive-letter
//     slicing would report C:'s filesystem instead of the stick's.
//   - GetVolumeInformationW returns the filesystem name as the driver
//     registers it: "NTFS", "FAT32", "FAT", "exFAT", "ReFS", "CDFS".
//     That is a positive identification, and it is what makes the FR-055
//     FAT32 refusal real on Windows rather than a warning.
//
// Free space comes from GetDiskFreeSpaceExW's lpFreeBytesAvailableToCaller,
// not lpTotalNumberOfFreeBytes: on a volume with disk quotas the two
// differ, and the instance can only write what is available to it.

// systemInspector reads the volume through the Win32 volume API.
type systemInspector struct{}

func (systemInspector) Inspect(path string) (Filesystem, Space, error) {
	target, err := nearestExisting(path)
	if err != nil {
		return Filesystem{}, Space{}, err
	}
	p, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return Filesystem{}, Space{}, fmt.Errorf("path %s: %w", target, err)
	}

	// The volume root of the target path.
	root := make([]uint16, windows.MAX_PATH+1)
	//nolint:gosec // G115: a fixed MAX_PATH+1 buffer, far below uint32.
	if err := windows.GetVolumePathName(p, &root[0], uint32(len(root))); err != nil {
		return Filesystem{}, Space{}, fmt.Errorf("GetVolumePathNameW %s: %w", target, err)
	}
	rootPtr := &root[0]

	f := Filesystem{Detection: "GetVolumeInformationW"}
	fsName := make([]uint16, windows.MAX_PATH+1)
	//nolint:gosec // G115: a fixed MAX_PATH+1 buffer, far below uint32.
	err = windows.GetVolumeInformation(rootPtr, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName)))
	if err != nil {
		// A volume that will not describe itself is reported as such: the
		// caller warns and proceeds, which is FR-055's "cannot be
		// identified" branch, not a refusal.
		f.Note = fmt.Sprintf("GetVolumeInformationW %s: %v", windows.UTF16ToString(root), err)
	} else {
		f = classify(windows.UTF16ToString(fsName), f.Detection)
	}

	var availableToCaller, total, free uint64
	space := Space{}
	if err := windows.GetDiskFreeSpaceEx(rootPtr, &availableToCaller, &total, &free); err == nil {
		space = Space{
			FreeBytes:  int64(availableToCaller), //nolint:gosec // G115: a volume's byte counts stay far below the int64 range
			TotalBytes: int64(total),             //nolint:gosec // G115: same
			Known:      true,
		}
	}
	return f, space, nil
}
