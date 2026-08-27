// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package preflight

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Known single-file ceilings, in bytes. Only filesystems whose ceiling
// this build is prepared to state appear here; everything else is
// reported unidentified, which warns rather than refuses (FR-055).
const (
	// maxFAT is the FAT32 ceiling FR-055 names: a file length is stored
	// in 32 bits, so the largest file is one byte short of 4 GiB. FAT12
	// and FAT16 share the field and cannot exceed it either.
	maxFAT int64 = 1<<32 - 1
	// maxExt is ext4's ceiling with the usual 4 KiB block size. ext2,
	// ext3 and ext4 share one superblock magic on Linux and cannot be
	// told apart from statfs alone; ext3's own ceiling is 2 TiB. The
	// generous figure is deliberate — this table decides REFUSALS, and
	// refusing a 3 TiB write on an ext4 volume because the number might
	// have been an ext3 one would ground a correct operation. A write
	// past a real ext3 ceiling still fails cleanly mid-transfer, store
	// intact (FR-055).
	maxExt int64 = 16 << 40
	// maxNTFS is the practical NTFS ceiling on modern cluster sizes.
	maxNTFS int64 = 8 << 50
	// maxHuge marks filesystems (exFAT, XFS, Btrfs, APFS, ZFS, ReFS)
	// whose ceiling is above anything an int64 byte count can express. It
	// is still a stated ceiling, not "unlimited": the comparison it feeds
	// is the same one every other entry feeds.
	maxHuge int64 = math.MaxInt64
)

// typeLimits maps a platform-reported filesystem type name, lowercased,
// onto its single-file ceiling. Absence means "this build does not know
// this filesystem", which is reported as such — never as capable.
//
// The keys cover the three platforms' spellings of the same filesystems:
// Linux's vfat/msdos, macOS's msdos, and Windows' FAT32 all land on the
// FAT ceiling.
var typeLimits = map[string]int64{
	"fat":   maxFAT,
	"fat12": maxFAT,
	"fat16": maxFAT,
	"fat32": maxFAT,
	"vfat":  maxFAT,
	"msdos": maxFAT,
	"exfat": maxHuge,
	"ntfs":  maxNTFS,
	"ntfs3": maxNTFS,
	"refs":  maxHuge,
	"ext2":  maxExt,
	"ext3":  maxExt,
	"ext4":  maxExt,
	"xfs":   maxHuge,
	"btrfs": maxHuge,
	"apfs":  maxHuge,
	"hfs":   maxHuge,
	"hfs+":  maxHuge,
	"zfs":   maxHuge,
	"f2fs":  maxHuge,
}

// classify turns a platform-reported type name into a Filesystem.
// A name this build has no ceiling for is reported identified: false with
// the name kept — an operator seeing "smbfs, capability unknown" learns
// more than one seeing nothing.
func classify(name, detection string) Filesystem {
	f := Filesystem{Type: name, Detection: detection}
	limit, ok := typeLimits[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		if name != "" {
			f.Note = "this build knows no single-file limit for this filesystem type"
		}
		return f
	}
	f.Identified = true
	f.MaxFileSize = limit
	return f
}

// nearestExisting walks path up to the first component that exists, so a
// target directory can be checked before it is created — an export names
// a file that is not there yet, and statfs on a missing path answers
// nothing at all.
//
// It stops at the filesystem root: an absolute path always terminates on
// something that exists, and a relative one terminates on ".".
func nearestExisting(path string) (string, error) {
	if path == "" {
		path = "."
	}
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("no existing ancestor of " + path + " could be inspected")
		}
		p = parent
	}
}
