// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// File is one member of the archive, as bytes.
type File struct {
	Name string
	Data []byte
}

// Source is one member's collector. A nil Source, or one that returns
// ErrOmitted (wrapped or not), leaves the member out and says why in the
// manifest; any other error aborts the bundle (TBY-DIA-001).
type Source func() ([]byte, error)

// ErrOmitted is returned by a Source that has nothing to give for a
// documented reason.
type ErrOmitted string

func (e ErrOmitted) Error() string { return string(e) }

// Sources are the collectors of one bundle. The instance fills every one;
// the command line on a host without a running instance fills what it can
// read from disk, and the rest is omitted with its reason.
type Sources struct {
	Version   Source
	Config    Source
	Logs      Source
	MediaLog  Source
	Metrics   Source
	Integrity Source
	State     Source
	// Tasks returns the reports of the most recent tasks, newest first,
	// at most n; each file is named tasks/<id>.json.
	Tasks func(n int) ([]File, error)
}

// Options parameterize one bundle.
type Options struct {
	// Secrets is what the instance knows to be secret. Required: a bundle
	// built without one would have nothing to check.
	Secrets *Secrets
	// Tasks bounds the task reports (diagnostics.tasks).
	Tasks int
	// Now injects the clock under test.
	Now func() time.Time
}

// Bundle is a built, checked archive, ready to be written.
type Bundle struct {
	Manifest Manifest
	files    []File
}

// maxMember bounds one member; a collector that returns more is cut —
// after it was scrubbed and searched whole, so the cut can never leave
// part of a secret behind it. A bundle is read by a person, and is meant
// to attach to a ticket. A variable for the tests only.
var maxMember = 32 << 20

// memberCut is appended to a member the bundle cut.
const memberCut = "\n[truncated by the bundle: member exceeded the size bound]\n"

// MaxArchive bounds the archive a bundle of tasks task reports can be: the
// bound a client downloading one applies (every member at its own bound,
// plus the manifest and the tar framing).
func MaxArchive(tasks int) int64 {
	return int64(len(Members())+max(tasks, 1)) * (int64(maxMember) + int64(len(memberCut)) + 1<<10)
}

// cutsOf returns the offsets in data where a truncation happened: where a
// cut mark begins.
func cutsOf(data []byte) []int {
	var cuts []int
	for _, mark := range [][]byte{[]byte(windowCut), []byte(memberCut)} {
		for off := 0; ; {
			i := bytes.Index(data[off:], mark)
			if i < 0 {
				break
			}
			cuts = append(cuts, off+i)
			off += i + len(mark)
		}
	}
	sort.Ints(cuts)
	return cuts
}

// tailMember says the member is the end of a longer document: its first
// bytes may be the end of something cut away.
func tailMember(name string) bool { return name == MemberMediaLog }

// freeForm are the members that carry text some other component wrote: an
// unknown secret may be in them, so they are scrubbed before the check.
// The others are structured documents Tobby writes itself; a secret in one
// of those is a defect, and the check aborts the bundle rather than quietly
// fixing it.
func freeForm(name string) bool {
	switch name {
	case MemberLogs, MemberMediaLog, MemberMetrics:
		return true
	}
	return len(name) > len(MemberTasks) && name[:len(MemberTasks)] == MemberTasks
}

// Build collects every member, scrubs the free-form ones, searches every
// member for the secrets the instance knows, and returns the bundle — or
// TBY-DIA-002 naming the member that still carried one (the bundle is
// then not produced at all), or TBY-DIA-001 when a member could not be
// collected.
func Build(opts Options, src *Sources) (*Bundle, error) {
	if opts.Secrets == nil {
		return nil, taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{
			"detail": "no set of secrets to check the archive against",
		})
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	fail := func(err error) error {
		return taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}

	var files []File
	var omitted []Omission
	add := func(name string, s Source) error {
		if s == nil {
			omitted = append(omitted, Omission{Name: name, Reason: "not available on this instance"})
			return nil
		}
		data, err := s()
		var om ErrOmitted
		switch {
		case errors.As(err, &om):
			omitted = append(omitted, Omission{Name: name, Reason: string(om)})
			return nil
		case err != nil:
			return fmt.Errorf("collecting %s: %w", name, err)
		}
		files = append(files, File{Name: name, Data: data})
		return nil
	}
	for _, m := range []struct {
		name string
		s    Source
	}{
		{MemberVersion, src.Version}, {MemberConfig, src.Config}, {MemberLogs, src.Logs},
		{MemberMediaLog, src.MediaLog}, {MemberMetrics, src.Metrics},
		{MemberIntegrity, src.Integrity}, {MemberState, src.State},
	} {
		if err := add(m.name, m.s); err != nil {
			return nil, fail(err)
		}
	}
	if src.Tasks != nil {
		n := opts.Tasks
		if n < 1 {
			n = 1
		}
		tasks, err := src.Tasks(n)
		if err != nil {
			return nil, fail(fmt.Errorf("collecting %s: %w", MemberTasks, err))
		}
		for _, t := range tasks {
			if !Known(t.Name) || t.Name == MemberTasks {
				return nil, fail(fmt.Errorf("task report %q is not a documented member", t.Name))
			}
			files = append(files, t)
		}
	} else {
		omitted = append(omitted, Omission{Name: MemberTasks, Reason: "not available on this instance"})
	}

	// Scrub the free-form members, then search EVERY member: this second
	// step is the one that fails closed. Both run on the member whole,
	// before the size bound cuts it, and both judge the marks where the
	// log window cut a record: a secret cut in two is still a secret.
	for i := range files {
		f := &files[i]
		if freeForm(f.Name) {
			f.Data = opts.Secrets.scrub(f.Data, cutsOf(f.Data), tailMember(f.Name), true)
		}
		if reason := opts.Secrets.find(f.Data, cutsOf(f.Data), tailMember(f.Name)); reason != "" {
			return nil, taxonomy.New(taxonomy.CodeDiagnosticsRedaction, taxonomy.Params{"member": f.Name})
		}
		if len(f.Data) > maxMember {
			f.Data = append(f.Data[:maxMember:maxMember], memberCut...)
			if freeForm(f.Name) {
				// Scrubbed whole already: only what the new cut left of a
				// known value can remain.
				f.Data = opts.Secrets.scrubKnown(f.Data, cutsOf(f.Data), false)
			}
			if reason := opts.Secrets.find(f.Data, cutsOf(f.Data), tailMember(f.Name)); reason != "" {
				return nil, taxonomy.New(taxonomy.CodeDiagnosticsRedaction, taxonomy.Params{"member": f.Name})
			}
		}
	}

	sort.SliceStable(files, func(i, j int) bool { return order(files[i].Name) < order(files[j].Name) })
	man := Manifest{Format: FormatVersion, Generated: now().UTC().Truncate(time.Second), Omitted: omitted}
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		man.Members = append(man.Members, Member{
			Name: f.Name, Size: int64(len(f.Data)), Digest: "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	if err := man.Validate(); err != nil {
		return nil, fail(err)
	}
	return &Bundle{Manifest: man, files: files}, nil
}

// order puts members in archive order: documented order, tasks by name.
func order(name string) string {
	for i, m := range Members() {
		if name == m {
			return fmt.Sprintf("%02d", i)
		}
	}
	return fmt.Sprintf("%02d%s", len(Members()), name)
}

// Files returns the members (without the manifest), for tests and for the
// callers that report on them.
func (b *Bundle) Files() []File { return append([]File(nil), b.files...) }

// WriteTo writes the archive as a gzip-compressed tar: the manifest first,
// then the members in documented order. Modification times are the
// manifest's, so the same inputs give the same bytes.
func (b *Bundle) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	gz := gzip.NewWriter(cw)
	tw := tar.NewWriter(gz)
	man, err := marshalManifest(&b.Manifest)
	if err != nil {
		return cw.n, err
	}
	write := func(name string, data []byte) error {
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: b.Manifest.Generated, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := write(MemberManifest, man); err != nil {
		return cw.n, err
	}
	for _, f := range b.files {
		if err := write(f.Name, f.Data); err != nil {
			return cw.n, err
		}
	}
	if err := tw.Close(); err != nil {
		return cw.n, err
	}
	if err := gz.Close(); err != nil {
		return cw.n, err
	}
	return cw.n, nil
}

// Archive is WriteTo into memory.
func (b *Bundle) Archive() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func marshalManifest(m *Manifest) ([]byte, error) {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
