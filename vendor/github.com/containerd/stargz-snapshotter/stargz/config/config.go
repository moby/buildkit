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

type Config struct {
	HTTPCacheType       string `toml:"http_cache_type"`
	FSCacheType         string `toml:"filesystem_cache_type"`
	ResolveResultEntry  int    `toml:"resolve_result_entry"`
	PrefetchSize        int64  `toml:"prefetch_size"`
	PrefetchTimeoutSec  int64  `toml:"prefetch_timeout_sec"`
	NoPrefetch          bool   `toml:"noprefetch"`
	Debug               bool   `toml:"debug"`
	AllowNoVerification bool   `toml:"allow_no_verification"`

	// ResolverConfig is config for resolving registries.
	ResolverConfig `toml:"resolver"`

	// BlobConfig is config for layer blob management.
	BlobConfig `toml:"blob"`

	// DirectoryCacheConfig is config for directory-based cache.
	DirectoryCacheConfig `toml:"directory_cache"`
}

type ResolverConfig struct {
	Host                map[string]HostConfig `toml:"host"`
	ConnectionPoolEntry int                   `toml:"connection_pool_entry"`
}

type HostConfig struct {
	Mirrors []MirrorConfig `toml:"mirrors"`
}

type MirrorConfig struct {
	Host     string `toml:"host"`
	Insecure bool   `toml:"insecure"`
}

type BlobConfig struct {
	ValidInterval int64 `toml:"valid_interval"`
	CheckAlways   bool  `toml:"check_always"`
	ChunkSize     int64 `toml:"chunk_size"`
}

type DirectoryCacheConfig struct {
	MaxLRUCacheEntry int  `toml:"max_lru_cache_entry"`
	MaxCacheFds      int  `toml:"max_cache_fds"`
	SyncAdd          bool `toml:"sync_add"`
}
