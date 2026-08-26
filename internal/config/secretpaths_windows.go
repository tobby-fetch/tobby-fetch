// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import "golang.org/x/sys/windows"

// volumeNameDOS is GetFinalPathNameByHandleW's FILE_NAME_NORMALIZED |
// VOLUME_NAME_DOS. Both are zero, and golang.org/x/sys/windows exports
// neither; a bare 0 at the call site would say nothing about which of the
// four volume-name forms is being asked for, and the answer is the whole
// point of the call.
const volumeNameDOS = 0

// canonicalVolume answers with the one path the operating system itself
// considers final for an existing directory or file (B-027, NFR-020).
//
// ordinaryVolume rewrites the spellings that are a matter of syntax.
// These are not:
//
//   - `subst X: C:\store` makes X:\ and C:\store the same directory under
//     two different volume names. filepath.Rel relates neither to the
//     other, so a credentials file configured as X:\creds.json was not
//     under the configured store root C:\store, and the refusal that
//     keeps secrets off a transported medium never fired.
//   - `\\localhost\C$\store` — the administrative share — is the same
//     again, reached over a UNC name.
//
// GetFinalPathNameByHandle with VOLUME_NAME_DOS collapses both onto the
// drive-letter form, because it asks the object manager what the handle
// actually refers to rather than parsing the string it was opened with.
//
// FILE_FLAG_BACKUP_SEMANTICS is what lets a DIRECTORY be opened at all;
// without it CreateFile refuses one, and the store root is a directory.
// Nothing is opened for reading or writing — the access mask is zero,
// which asks only for the handle — so this cannot disturb a file another
// process holds.
//
// It answers the input unchanged when the path cannot be opened. That is
// the honest failure: the caller has already resolved as far as the
// filesystem allowed, and inventing a canonical form for something the
// operating system would not name would be worse than saying nothing.
func canonicalVolume(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return path
	}
	defer windows.CloseHandle(h) //nolint:errcheck // a handle opened for no access; nothing is buffered on it

	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameDOS)
		if err != nil {
			return path
		}
		if int(n) < len(buf) {
			// The returned name carries the extended-length prefix, which
			// is exactly the spelling ordinaryVolume exists to remove.
			return ordinaryVolume(windows.UTF16ToString(buf[:n]))
		}
		// n is the required length, prefix included: grow and ask again.
		buf = make([]uint16, n+1)
	}
}
