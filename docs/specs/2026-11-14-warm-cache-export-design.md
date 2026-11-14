# Warm remote-cache export optimization

## Problem

When an unchanged build imports a remote cache and exports a `mode=max` cache,
BuildKit currently materializes remote results while preparing the export even
when the imported remote already has usable blobs. Issue
[#7370](https://github.com/moby/buildkit/issues/7370) reports 14–17 seconds of
preparation time for a 620-vertex graph. The cost grows with the number of
records and includes content-store and database work.

## Goal and non-goals

Avoid full result materialization for compatible remote records during cache
export. Preserve the current behavior for missing content and cases requiring
conversion or local resolution.

This change does not redesign remote cache formats, change cache trust
assumptions, or optimize the separate cost of sending blobs to remote cache
services.

## Design

`solver/exporter.go` already asks result storage for remote descriptors before
loading a result. Use that remote when its descriptors meet the requested
compression requirements and can be confirmed available, instead of always
calling `Load` when compression options are present. Descriptor availability
checks must not materialize layer contents.

Keep the existing load/resolve path when no compatible remote exists or when
compression/conversion requires local content. Preserve descriptor annotations
needed by registry export, especially the source reference used for
cross-repository mounts. A missing blob must retain the current fallback
behavior and must not be advertised as an available cache result.

Implementation should follow the existing storage and worker abstractions.
Avoid adding a public API or changing cache record formats unless code
inspection shows there is no safe internal alternative.

## Correctness requirements

- A compatible, available remote result can be exported without fully loading
  its layers.
- Missing descriptors trigger the existing behavior, including the case
  covered by `testCacheExportCacheDeletedContent`.
- Compression mismatch and forced conversion continue through a path that
  produces the requested output.
- Registry source annotations remain available for cross-repository blob
  mounting.
- Exported cache records and build outputs remain compatible with current
  behavior.

## Validation

Add focused tests for the exporter decision and descriptor availability. Extend
existing integration coverage where needed for missing local-cache blobs,
compression handling, and registry source annotations.

Run the relevant package tests and the focused integration suites. Measure a
warm `mode=max` export using the issue's 620-vertex shape before and after,
separating cache preparation from cache sending. Record both timings and avoid
claiming a speedup if only the unrelated sending phase was measured.

## Evidence and constraints

- [BuildKit issue #7370](https://github.com/moby/buildkit/issues/7370), including
  its profile, reproducer, and maintainer analysis.
- [Build Systems à la Carte](https://doi.org/10.1145/3236774): keep cache,
  execution, and scheduling decisions explicit rather than coupling the
  optimization to unrelated solver behavior.
- [Detecting Build Dependency Errors in Incremental Builds](https://doi.org/10.1145/3650212.3652105):
  exercise both changed and unchanged inputs so cache correctness is checked
  alongside incremental reuse.
