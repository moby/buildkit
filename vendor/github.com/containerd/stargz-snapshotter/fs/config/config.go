/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

/*
   Copyright 2019 The Go Authors. All rights reserved.
   Use of this source code is governed by a BSD-style
   license that can be found in the NOTICE.md file.
*/

package config

const (
	// TargetSkipVerifyLabel is a snapshot label key that indicates to skip content
	// verification for the layer.
	TargetSkipVerifyLabel = "containerd.io/snapshot/remote/stargz.skipverify"

	// TargetPrefetchSizeLabel is a snapshot label key that indicates size to prefetch
	// the layer. If the layer is eStargz and contains prefetch landmarks, these config
	// will be respeced.
	TargetPrefetchSizeLabel = "containerd.io/snapshot/remote/stargz.prefetch"
)

// Config is configuration for stargz snapshotter filesystem.
type Config struct {
	// Type of cache for compressed contents fetched from the registry. "memory" stores them on memory.
	// Other values default to cache them on disk.
	HTTPCacheType string `toml:"http_cache_type" json:"http_cache_type"`

	// Type of cache for uncompressed files contents. "memory" stores them on memory. Other values
	// default to cache them on disk.
	FSCacheType string `toml:"filesystem_cache_type" json:"filesystem_cache_type"`

	// ResolveResultEntryTTLSec is TTL (in sec) to cache resolved layers for
	// future use. (default 120s)
	ResolveResultEntryTTLSec int `toml:"resolve_result_entry_ttl_sec" json:"resolve_result_entry_ttl_sec"`

	// PrefetchSize is the default size (in bytes) to prefetch when mounting a layer. Default is 0. Stargz-snapshotter still
	// uses the value specified by the image using "containerd.io/snapshot/remote/stargz.prefetch" or the landmark file.
	PrefetchSize int64 `toml:"prefetch_size" json:"prefetch_size"`

	// PrefetchTimeoutSec is the default timeout (in seconds) when the prefetching takes long. Default is 10s.
	PrefetchTimeoutSec int64 `toml:"prefetch_timeout_sec" json:"prefetch_timeout_sec"`

	// NoPrefetch disables prefetching. Default is false.
	NoPrefetch bool `toml:"noprefetch" json:"noprefetch"`

	// NoBackgroundFetch disables the behaviour of fetching the entire layer contents in background. Default is false.
	NoBackgroundFetch bool `toml:"no_background_fetch" json:"no_background_fetch"`

	// Debug enables filesystem debug log.
	Debug bool `toml:"debug" json:"debug"`

	// AllowNoVerification allows mouting images without verification. Default is false.
	AllowNoVerification bool `toml:"allow_no_verification" json:"allow_no_verification"`

	// DisableVerification disables verifying layer contents. Default is false.
	DisableVerification bool `toml:"disable_verification" json:"disable_verification"`

	// MaxConcurrency is max number of concurrent background tasks for fetching layer contents. Default is 2.
	MaxConcurrency int64 `toml:"max_concurrency" json:"max_concurrency"`

	// NoPrometheus disables exposing filesystem-related metrics. Default is false.
	NoPrometheus bool `toml:"no_prometheus" json:"no_prometheus"`

	// BlobConfig is config for layer blob management.
	BlobConfig `toml:"blob" json:"blob"`

	// DirectoryCacheConfig is config for directory-based cache.
	DirectoryCacheConfig `toml:"directory_cache" json:"directory_cache"`

	// FuseConfig is configurations for FUSE fs.
	FuseConfig `toml:"fuse" json:"fuse"`

	// ResolveResultEntry is a deprecated field.
	ResolveResultEntry int `toml:"resolve_result_entry" json:"resolve_result_entry"` // deprecated
}

// BlobConfig is configuration for the logic to fetching blobs.
type BlobConfig struct {
	// ValidInterval specifies a duration (in seconds) during which the layer can be reused without
	// checking the connection to the registry. Default is 60.
	ValidInterval int64 `toml:"valid_interval" json:"valid_interval"`

	// CheckAlways overwrites ValidInterval to 0 if it's true. Default is false.
	CheckAlways bool `toml:"check_always" json:"check_always"`

	// ChunkSize is the granularity (in bytes) at which background fetch and on-demand reads
	// are fetched from the remote registry. Default is 50000.
	ChunkSize int64 `toml:"chunk_size" json:"chunk_size"`

	// FetchTimeoutSec is a timeout duration (in seconds) for fetching chunks from the registry. Default is 300.
	FetchTimeoutSec int64 `toml:"fetching_timeout_sec" json:"fetching_tieout_sec"`

	// ForceSingleRangeMode disables using of multiple ranges in a Range Request and always specifies one larger
	// region that covers them. Default is false.
	ForceSingleRangeMode bool `toml:"force_single_range_mode" json:"force_single_range_mode"`

	// PrefetchChunkSize is the maximum bytes transferred per http GET from remote registry
	// during prefetch. It is recommended to have PrefetchChunkSize > ChunkSize.
	// If PrefetchChunkSize < ChunkSize prefetch bytes will be fetched as a single http GET,
	// else total GET requests for prefetch = ceil(PrefetchSize / PrefetchChunkSize).
	// Default is 0.
	PrefetchChunkSize int64 `toml:"prefetch_chunk_size" json:"prefetch_chunk_size"`

	// MaxRetries is a max number of reries of a HTTP request. Default is 5.
	MaxRetries int `toml:"max_retries" json:"max_retries"`

	// MinWaitMSec is minimal delay (in seconds) for the next retrying after a request failure. Default is 30.
	MinWaitMSec int `toml:"min_wait_msec" json:"min_wait_msec"`

	// MinWaitMSec is maximum delay (in seconds) for the next retrying after a request failure. Default is 30.
	MaxWaitMSec int `toml:"max_wait_msec" json:"max_wait_msec"`
}

// DirectoryCacheConfig is configuration for the disk-based cache.
type DirectoryCacheConfig struct {
	// MaxLRUCacheEntry is the number of entries of LRU cache to cache data on memory. Default is 10.
	MaxLRUCacheEntry int `toml:"max_lru_cache_entry" json:"max_lru_cache_entry"`

	// MaxCacheFds is the number of entries of LRU cache to hold fds of files of cached contents. Default is 10.
	MaxCacheFds int `toml:"max_cache_fds" json:"max_cache_fds"`

	// SyncAdd being true means that each adding of data to the cache blocks until the data is fully written to the
	// cache directory. Default is false.
	SyncAdd bool `toml:"sync_add" json:"sync_add"`

	// Direct disables on-memory data cache. Default is true for saving memory usage.
	Direct bool `toml:"direct" default:"true" json:"direct"`

	// FadvDontNeed forcefully clean fscache pagecache for saving memory. Default is false.
	FadvDontNeed bool `toml:"fadv_dontneed" json:"fadv_dontneed"`
}

// FuseConfig is configuration for FUSE fs.
type FuseConfig struct {
	// AttrTimeout defines overall timeout attribute for a file system in seconds.
	AttrTimeout int64 `toml:"attr_timeout" json:"attr_timeout"`

	// EntryTimeout defines TTL for directory, name lookup in seconds.
	EntryTimeout int64 `toml:"entry_timeout" json:"entry_timeout"`

	// PassThrough indicates whether to enable FUSE passthrough mode to improve local file read performance. Default is false.
	PassThrough bool `toml:"passthrough" default:"false" json:"passthrough"`

	// MergeBufferSize is the size of the buffer to merge chunks (in bytes) for passthrough mode. Default is 400MB.
	MergeBufferSize int64 `toml:"merge_buffer_size" default:"419430400" json:"merge_buffer_size"`

	// MergeWorkerCount is the number of workers to merge chunks for passthrough mode. Default is 10.
	MergeWorkerCount int `toml:"merge_worker_count" default:"10" json:"merge_worker_count"`
}
