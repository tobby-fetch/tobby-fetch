// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package preflight answers the two questions FR-055 asks before a mirror
// synchronization or an export starts: does it fit, and will it go
// through.
//
// "Does it fit" is arithmetic over free space with a configurable safety
// margin. "Will it go through" is not: a filesystem that cannot hold a
// file larger than 4 GiB accepts every byte until the one that breaks it,
// and the failure lands hours into a transfer, on a medium somebody is
// waiting for. So the filesystem is inspected first, and the operation is
// refused before anything moves.
//
// The inspection is deliberately honest. Linux, macOS and Windows do not
// expose the same facts, and none of them exposes them for every mount:
// this package positively identifies what the platform names, states that
// it does not know when the platform says nothing, and never assumes a
// filesystem is capable by default. An unidentified target warns and
// proceeds — refusing it would ground the product on every filesystem
// this build has not heard of — but the warning travels all the way to
// the operator, because "we could not check" and "we checked and it is
// fine" are different sentences.
package preflight

import (
	"fmt"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// DefaultMarginPercent is the FR-055 safety margin applied to the free
// space of a target: the projection must fit in free space MINUS this
// share of it. Ten percent, because a store is not the only writer on its
// volume — logs, the container runtime and the operating system keep
// writing while a four-hour transfer runs — and a projection that lands
// exactly on the last free byte lands on a full disk.
const DefaultMarginPercent = 10

// Filesystem is what this build could learn about the filesystem holding
// one path.
//
// Identified is the field the refusals hinge on, and it is deliberately
// not derivable from Name: a platform can name a filesystem this build
// knows nothing about ("zfs" on FreeBSD), and a name without known limits
// is not an identification. MaxFileSize is 0 for "unknown" and never for
// "unlimited": no filesystem is unlimited, and a zero that means "go
// ahead" is the kind of default this package exists to avoid.
type Filesystem struct {
	// Type is the filesystem type as the platform names it — "ext4",
	// "apfs", "msdos", "FAT32". Empty when the platform said nothing.
	Type string `json:"type,omitempty"`
	// Identified reports that Type was read AND that this build knows the
	// file-size limit of that type. False is the honest default.
	Identified bool `json:"identified"`
	// MaxFileSize is the largest single file the filesystem can hold, in
	// bytes. Zero means unknown — never unlimited.
	MaxFileSize int64 `json:"max_file_size,omitempty"`
	// Detection names the mechanism that produced Type, so a report says
	// where the fact came from ("statfs f_type", "GetVolumeInformationW").
	Detection string `json:"detection,omitempty"`
	// Note carries the reason Identified is false, when there is one to
	// give (an unreadable mount, a type this build does not know).
	Note string `json:"note,omitempty"`
}

// Space is the free-space picture of a target volume.
type Space struct {
	FreeBytes  int64 `json:"free_bytes"`
	TotalBytes int64 `json:"total_bytes"`
	// Known is false when the platform could not answer at all; the space
	// arithmetic is then skipped rather than run on zeros, which would
	// refuse every operation on a volume nobody could measure.
	Known bool `json:"known"`
}

// Inspector reads the filesystem facts of a directory. The production
// implementation is per-platform (fs_linux.go, fs_darwin.go,
// fs_windows.go, fs_unsupported.go); tests inject their own.
//
// The seam exists for one reason: FR-055's acceptance criterion is "a
// FAT32 target with a > 4 GiB blob is refused naming the limit", and a
// requirement whose only test needs a FAT32 volume mounted on the build
// machine is a requirement nobody runs. Injecting the layer makes the
// refusal itself executable everywhere; the real detectors are covered
// separately, against the volume the tests actually run on.
type Inspector interface {
	// Inspect reports the filesystem and free space of the volume holding
	// path. path need not exist; the nearest existing ancestor answers,
	// which is what lets an export be checked before its directory is
	// created. An error means nothing could be read at all.
	Inspect(path string) (Filesystem, Space, error)
}

// System is the platform inspector. A variable so tests can substitute
// one; production never assigns it.
var System Inspector = systemInspector{}

// Target names what a check is about, for the messages: the local store
// of a synchronization, or the archive of an export.
type Target string

// The two targets FR-055 names.
const (
	// TargetStore is the local store a synchronization writes into.
	TargetStore Target = "store"
	// TargetArchive is the export archive (or layout directory) an export
	// writes. Its largest file is the archive itself when the export
	// produces a single tar — which is precisely the case FR-055 calls
	// out, because a 4 GiB ceiling that every individual blob clears is
	// still a ceiling the archive does not.
	TargetArchive Target = "archive"
)

// Request is one pre-flight question.
type Request struct {
	// Target says what is being written, for the refusal messages.
	Target Target
	// Path is the directory the bytes land in. It need not exist yet.
	Path string
	// ProjectedBytes is what the operation would ADD to the target — the
	// deduplicated, already-present-net figure of FR-055, not the gross
	// size of the source.
	ProjectedBytes int64
	// LargestFileBytes is the biggest single file that would be written.
	// For a store that is the largest blob; for a single-tar export it is
	// the archive itself. Zero skips the file-size verdict.
	LargestFileBytes int64
	// MarginPercent is the safety margin; zero uses DefaultMarginPercent.
	// A negative value is rejected by configuration validation long
	// before it reaches here.
	MarginPercent int
}

// Warning is a non-blocking finding of a pre-flight check. Codes are
// stable strings: the UI localizes them (FR-063), the API and the CLI
// carry them raw (ADR-0015 §7).
type Warning string

// The pre-flight warnings.
const (
	// WarnFilesystemUnidentified is FR-055's "cannot be identified" case:
	// the operation proceeds, and the operator is told that the file-size
	// capability of the target was never established.
	WarnFilesystemUnidentified Warning = "filesystem-unidentified"
	// WarnSpaceUnknown reports a volume whose free space the platform
	// would not answer. The margin arithmetic is skipped: refusing on an
	// unmeasured volume would be a guess, and this package does not
	// guess in either direction.
	WarnSpaceUnknown Warning = "space-unknown"
)

// Check is the serializable verdict of one pre-flight question — the
// structure the plan report embeds, the API returns, and the media screen
// (R-02) consumes.
type Check struct {
	Target     Target     `json:"target"`
	Path       string     `json:"path"`
	Filesystem Filesystem `json:"filesystem"`
	Space      Space      `json:"space"`
	// MarginPercent is the margin actually applied, defaults resolved.
	MarginPercent int `json:"margin_percent"`
	// ReservedBytes is the margin in bytes, UsableBytes the free space
	// left after it. Both are reported rather than left to be recomputed:
	// the whole point of the refusal is that the operator can check the
	// arithmetic that produced it.
	ReservedBytes    int64 `json:"reserved_bytes"`
	UsableBytes      int64 `json:"usable_bytes"`
	ProjectedBytes   int64 `json:"projected_bytes"`
	LargestFileBytes int64 `json:"largest_file_bytes"`
	// ShortfallBytes is the exact number of bytes missing, zero when the
	// projection fits (FR-055: "stating the shortfall").
	ShortfallBytes int64 `json:"shortfall_bytes"`
	// Warnings are the non-blocking findings, in a stable order.
	Warnings []Warning `json:"warnings,omitempty"`
	// RefusalCode is the taxonomy code of the refusal, empty when the
	// operation may start. The rendered error itself is not carried here:
	// a report is stored and re-read, and a localized sentence in it
	// would be frozen in the language of whoever ran the check (R-03).
	RefusalCode taxonomy.Code `json:"refusal_code,omitempty"`
}

// OK reports whether the operation may start.
func (c *Check) OK() bool { return c.RefusalCode == "" }

// Evaluate answers one pre-flight question against an inspector.
//
// It returns the check unconditionally — a refused operation still has a
// report to show — and the refusal as a taxonomy error, nil when the
// operation may start. The two carry the same verdict on purpose: the
// error is what a caller returns, the check is what a screen renders.
//
// Order matters. The file-size verdict runs FIRST: a FAT32 volume with
// terabytes free is still a volume that cannot hold the blob, and
// reporting "not enough space" for it would send an operator to buy a
// bigger disk for a problem a bigger disk does not fix.
func Evaluate(in Inspector, req Request) (Check, *taxonomy.Error) {
	margin := req.MarginPercent
	if margin <= 0 {
		margin = DefaultMarginPercent
	}
	c := Check{
		Target:           req.Target,
		Path:             req.Path,
		MarginPercent:    margin,
		ProjectedBytes:   req.ProjectedBytes,
		LargestFileBytes: req.LargestFileBytes,
	}

	fs, space, err := in.Inspect(req.Path)
	if err != nil {
		// An unreadable target is not a verdict about capacity: say so,
		// warn on both counts, and let the operation proceed to the write
		// that will produce the real error. Refusing here would ground an
		// instance on any platform whose statfs this build cannot call.
		c.Filesystem = Filesystem{Note: err.Error()}
		c.Warnings = append(c.Warnings, WarnFilesystemUnidentified, WarnSpaceUnknown)
		return c, nil
	}
	c.Filesystem, c.Space = fs, space

	if !fs.Identified {
		c.Warnings = append(c.Warnings, WarnFilesystemUnidentified)
	} else if req.LargestFileBytes > 0 && fs.MaxFileSize > 0 && req.LargestFileBytes > fs.MaxFileSize {
		c.RefusalCode = taxonomy.CodeFileTooLarge
		return c, taxonomy.New(taxonomy.CodeFileTooLarge, taxonomy.Params{
			"path":       req.Path,
			"filesystem": fs.Type,
			"limit":      fmt.Sprintf("%d", fs.MaxFileSize),
			"size":       fmt.Sprintf("%d", req.LargestFileBytes),
			"what":       string(req.Target),
		})
	}

	if !space.Known {
		c.Warnings = append(c.Warnings, WarnSpaceUnknown)
		return c, nil
	}
	c.ReservedBytes = space.FreeBytes / 100 * int64(margin)
	c.UsableBytes = space.FreeBytes - c.ReservedBytes
	if req.ProjectedBytes > c.UsableBytes {
		c.ShortfallBytes = req.ProjectedBytes - c.UsableBytes
		c.RefusalCode = taxonomy.CodeInsufficientSpace
		return c, taxonomy.New(taxonomy.CodeInsufficientSpace, taxonomy.Params{
			"path":      req.Path,
			"needed":    fmt.Sprintf("%d", req.ProjectedBytes),
			"available": fmt.Sprintf("%d", c.UsableBytes),
			"shortfall": fmt.Sprintf("%d", c.ShortfallBytes),
			"margin":    fmt.Sprintf("%d", margin),
			"free":      fmt.Sprintf("%d", space.FreeBytes),
		})
	}
	return c, nil
}
