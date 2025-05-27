package workers

import (
	"testing"

	"github.com/moby/buildkit/util/testutil/integration"
)

const (
	FeatureCacheExport          = "cache_export"
	FeatureCacheImport          = "cache_import"
	FeatureCacheBackendAzblob   = "cache_backend_azblob"
	FeatureCacheBackendGha      = "cache_backend_gha"
	FeatureCacheBackendInline   = "cache_backend_inline"
	FeatureCacheBackendLocal    = "cache_backend_local"
	FeatureCacheBackendRegistry = "cache_backend_registry"
	FeatureCacheBackendS3       = "cache_backend_s3"
	FeatureCacheBackendGCS      = "cache_backend_gcs"
	FeatureDirectPush           = "direct_push"
	FeatureFrontendOutline      = "frontend_outline"
	FeatureFrontendTargets      = "frontend_targets"
	FeatureImageExporter        = "image_exporter"
	FeatureInfo                 = "info"
	FeatureMergeDiff            = "merge_diff"
	FeatureMultiCacheExport     = "multi_cache_export"
	FeatureMultiPlatform        = "multi_platform"
	FeatureOCIExporter          = "oci_exporter"
	FeatureOCILayout            = "oci_layout"
	FeatureProvenance           = "provenance"
	FeatureSBOM                 = "sbom"
	FeatureSecurityMode         = "security_mode"
	FeatureSourceDateEpoch      = "source_date_epoch"
	FeatureCNINetwork           = "cni_network"
	FeatureContentCheck         = "content_check"
	FeatureCDI                  = "cdi"
)

var features = map[string]struct{}{
	FeatureCacheExport:          {},
	FeatureCacheImport:          {},
	FeatureCacheBackendAzblob:   {},
	FeatureCacheBackendGha:      {},
	FeatureCacheBackendInline:   {},
	FeatureCacheBackendLocal:    {},
	FeatureCacheBackendRegistry: {},
	FeatureCacheBackendS3:       {},
	FeatureCacheBackendGCS:      {},
	FeatureDirectPush:           {},
	FeatureFrontendOutline:      {},
	FeatureFrontendTargets:      {},
	FeatureImageExporter:        {},
	FeatureInfo:                 {},
	FeatureMergeDiff:            {},
	FeatureMultiCacheExport:     {},
	FeatureMultiPlatform:        {},
	FeatureOCIExporter:          {},
	FeatureOCILayout:            {},
	FeatureProvenance:           {},
	FeatureSBOM:                 {},
	FeatureSecurityMode:         {},
	FeatureSourceDateEpoch:      {},
	FeatureCNINetwork:           {},
	FeatureContentCheck:         {},
	FeatureCDI:                  {},
}

func CheckFeatureCompat(t *testing.T, sb integration.Sandbox, reason ...string) {
	integration.CheckFeatureCompat(t, sb, features, reason...)
}

func HasFeatureCompat(t *testing.T, sb integration.Sandbox, reason ...string) error {
	return integration.HasFeatureCompat(t, sb, features, reason...)
}
