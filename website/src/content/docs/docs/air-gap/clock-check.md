---
title: Detect a wrong clock
description: Why an isolated zone cannot trust its clock, the two dates Tobby compares it with, and the five places an implausible clock is shown — without correcting it.
sidebar:
  order: 10
---

An air-gapped zone has no time source to trust, and a wrong clock quietly
falsifies everything judged against it: the age of the vulnerability database,
the staleness guard on media, the expiry of tokens and of exceptions, the
validity of certificates. Tobby cannot know the right time. It does hold two
dates it can prove have passed, without any network, and it checks the local
clock against them.

## The two anchors

| Anchor | Where it comes from | Checked |
|---|---|---|
| **The build** | The build time of the binary (`SOURCE_DATE_EPOCH` of the reproducible build). | At startup. |
| **The medium** | The resolution time written in the medium's manifest. | Every time a medium is verified or imported — `tobby media verify`, `tobby media import`, the Media screen, the API. |

A local clock **earlier** than an anchor by more than `clock.skewTolerance`
(default `1h`, between `0` and `7d`) is **implausible**. The tolerance absorbs
ordinary drift between zones. Equal to the tolerance is still plausible. A
development build carries no build time and proves nothing: it records no
build verdict.

## What is shown

When the clock is implausible, Tobby says so in five places and **changes
nothing else**:

- **A log record** at warning level in the startup log (or the verification's
  log), with the code [`TBY-CLK-001`](../../reference/errors/#tby-clk-001)
  (build) or [`TBY-CLK-002`](../../reference/errors/#tby-clk-002) (medium,
  naming it), the gap and the tolerance.
- **A banner** on every page of the interface, naming the code, the local time,
  the gap and the anchor. It is read at each page: it disappears by itself when
  a later check is plausible, with no dismissal and no restart.
- **The command line**: `tobby media verify` and `tobby media import` print the
  warning on the error output — the report on the standard output stays the
  report — and the verdict is unchanged.
- **A metric**: `tobby_clock_implausible{anchor="build|medium"}`, `1` or `0`,
  exposed once the anchor has been checked. There is an [alert](../../passthrough/monitoring/).
- **The audit log**: a `clock.implausible` event, whose target is `build` or the
  medium and whose detail carries the gap, the tolerance and the code.

`GET /api/v1/clock` returns the latest verdict of each anchor to any signed-in
role.

## What it does not do

It does not correct the clock, and it does not make any decision. Expiries and
freshness checks keep being judged against the local clock, now visibly in
doubt: a medium is not refused because of it, an exception does not expire
earlier. A clock that is *later* than the truth is not detected — nothing the
instance holds proves how late it is. A medium's resolution time is itself
written by the source zone's clock: if that one was wrong, the check is
misleading in the other direction, which is why the warning names the medium.

## What to do

Check the host's clock against a trusted reference of the zone — a local time
server, or a hand-set value from a trusted source — then restart for the build
check or verify the medium again. If the **medium** is the one in advance, the
source zone's clock is the suspect: say so to whoever produced it.
