// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package scan is the scanner-neutral seam of ADR-0008 decision 5: the
// vocabulary vulnerability scanning, policy evaluation, reporting and
// metrics share, and nothing else (FR-031, FR-032, milestone 6).
//
// # What lives here, and what does not
//
// The policy engine never consumes a scanner's native types. A scanner
// sits behind the Scanner interface — a content digest in, normalized
// findings out, with the metadata of the database they were matched
// against — and everything downstream of it (the evaluated policy, the
// per-item task report, the API, the metrics) operates on the types of
// this package only. Trivy is the reference and only built-in
// implementation for v1.0 (ADR-0008 decision 4: a pinned external binary,
// run out of process); additional scanners and upstream VEX consumption
// plug in at this same seam, as additive v1.x extensions.
//
// The combination of the three policy levels (Combine) and the evaluation
// of one result against it (Evaluate, NotScanned) live here, pure and
// scanner-neutral. The Trivy adapter is package scan/trivy.
//
// # The policy, as the SRS fixes it (FR-031 amendment 2026-11-11)
//
//   - Mode is advisory ("warn") unless the instance says "block" — or a
//     Retriever or a recipe poses a threshold: a threshold posed for a
//     perimeter is blocking for that perimeter (milestone-6 opening,
//     correction of 2026-11-11). A recipe can make its own evaluation
//     blocking, never advisory.
//   - A finding violates the policy when its CVSS base score is >= the
//     threshold. Several sources: the highest score counts. No score: the
//     lower bound of its severity band (LowerBound). UNKNOWN: never
//     compared with the threshold, always reported as unscored.
//   - Threshold: the most specific level wins — recipe, then Retriever,
//     then instance — but a recipe can only LOWER the value the two others
//     produce; a higher recipe value is ignored and reported.
//   - Exceptions: instance and Retriever lists add up; a recipe carries
//     none. An exception past its expiry no longer applies and is
//     reported as expired.
//
// # Persistence
//
// A Report is persisted inside the task it belongs to (package tasks) and
// served verbatim by the API: its JSON form is part of the task schema,
// and it carries codes and parameters, never sentences (ADR-0015 §4), so
// it re-renders in the reader's language long after the fact.
package scan
