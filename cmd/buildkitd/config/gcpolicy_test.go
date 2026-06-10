package config

import (
	"testing"

	"github.com/containerd/containerd/v2/pkg/filters"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/disk"
	"github.com/stretchr/testify/require"
)

func TestDefaultGCPolicyFiltersMatch(t *testing.T) {
	policies := DefaultGCPolicy(GCConfig{}, disk.DiskStat{Total: 1e12})
	require.NotEmpty(t, policies)

	rule := policies[0]
	require.NotEmpty(t, rule.Filters)

	f, err := filters.ParseAll(rule.Filters...)
	require.NoError(t, err)

	adapt := func(rt client.UsageRecordType) filters.Adaptor {
		return filters.AdapterFunc(func(fieldpath []string) (string, bool) {
			if len(fieldpath) == 1 && fieldpath[0] == "type" {
				return string(rt), rt != ""
			}
			return "", false
		})
	}

	for _, rt := range []client.UsageRecordType{
		client.UsageRecordTypeLocalSource,
		client.UsageRecordTypeCacheMount,
		client.UsageRecordTypeGitCheckout,
	} {
		require.Truef(t, f.Match(adapt(rt)),
			"first default GC policy should match record type %q", rt)
	}

	// It must not match unrelated records such as regular build cache.
	require.Falsef(t, f.Match(adapt(client.UsageRecordTypeRegular)),
		"first default GC policy should not match record type %q", client.UsageRecordTypeRegular)
}
