// Package zstdchunked implements a writer for the zstd:chunked layer format.
//
// A zstd:chunked blob is a regular zstd stream where every file in the tar
// archive is compressed as one or more independent zstd frames. A table of
// contents (TOC) describing every file and its offset, and the tar-split data
// needed to reconstruct the original tar stream bit-for-bit, are appended as
// zstd skippable frames, followed by a fixed-size footer:
//
//	[FILE_1][FILE_2]..[FILE_N][SKIPPABLE FRAME: TOC][SKIPPABLE FRAME: TAR-SPLIT][SKIPPABLE FRAME: FOOTER]
//
// Decoders that are not aware of the format ignore the skippable frames, so
// decompressing a zstd:chunked blob yields exactly the original tar stream.
// Consumers that understand the format (e.g. containers/storage, used by
// Podman, Buildah and CRI-O) use the TOC to fetch only the files they are
// missing locally.
//
// The format is specified at
// https://github.com/containers/container-libs/blob/main/storage/docs/containers-storage-zstd-chunked.md
//
// The implementation is derived from go.podman.io/storage/pkg/chunked
// (formerly github.com/containers/storage/pkg/chunked), licensed under the
// Apache License, Version 2.0. It is kept here so that BuildKit does not have
// to depend on the containers/storage module, and is reduced to the parts
// needed for writing blobs.
package zstdchunked
