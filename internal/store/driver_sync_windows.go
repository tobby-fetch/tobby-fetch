// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

// syncDir does nothing on Windows, and the nothing is the point.
//
// The Unix half flushes the directory entry after a rename so an
// interruption cannot leave a name pointing at nothing (NFR-010). Windows
// has no equivalent call: os.File.Sync is FlushFileBuffers, which needs
// write access and is not defined on a directory handle — asking for it
// returns ERROR_ACCESS_DENIED, which the library's own PutContent turns
// into a failed write on every manifest and every tag (B-023).
//
// Nothing is lost by returning here. NTFS journals metadata operations,
// so the rename itself is what reaches the disk in order; there is no
// second step to force.
func syncDir(string) error { return nil }
