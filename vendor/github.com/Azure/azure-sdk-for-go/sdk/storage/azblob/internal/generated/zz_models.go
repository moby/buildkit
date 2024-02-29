//go:build go1.18
// +build go1.18

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See License.txt in the project root for license information.
// Code generated by Microsoft (R) AutoRest Code Generator. DO NOT EDIT.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

package generated

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"time"
)

// AccessPolicy - An Access policy
type AccessPolicy struct {
	// the date-time the policy expires
	Expiry *time.Time `xml:"Expiry"`

	// the permissions for the acl policy
	Permission *string `xml:"Permission"`

	// the date-time the policy is active
	Start *time.Time `xml:"Start"`
}

// ArrowConfiguration - Groups the settings used for formatting the response if the response should be Arrow formatted.
type ArrowConfiguration struct {
	// REQUIRED
	Schema []*ArrowField `xml:"Schema>Field"`
}

// ArrowField - Groups settings regarding specific field of an arrow schema
type ArrowField struct {
	// REQUIRED
	Type      *string `xml:"Type"`
	Name      *string `xml:"Name"`
	Precision *int32  `xml:"Precision"`
	Scale     *int32  `xml:"Scale"`
}

type BlobFlatListSegment struct {
	// REQUIRED
	BlobItems []*BlobItem `xml:"Blob"`
}

type BlobHierarchyListSegment struct {
	// REQUIRED
	BlobItems    []*BlobItem   `xml:"Blob"`
	BlobPrefixes []*BlobPrefix `xml:"BlobPrefix"`
}

// BlobItem - An Azure Storage blob
type BlobItem struct {
	// REQUIRED
	Deleted *bool `xml:"Deleted"`

	// REQUIRED
	Name *string `xml:"Name"`

	// REQUIRED; Properties of a blob
	Properties *BlobProperties `xml:"Properties"`

	// REQUIRED
	Snapshot *string `xml:"Snapshot"`

	// Blob tags
	BlobTags         *BlobTags `xml:"Tags"`
	HasVersionsOnly  *bool     `xml:"HasVersionsOnly"`
	IsCurrentVersion *bool     `xml:"IsCurrentVersion"`

	// Dictionary of
	Metadata map[string]*string `xml:"Metadata"`

	// Dictionary of
	OrMetadata map[string]*string `xml:"OrMetadata"`
	VersionID  *string            `xml:"VersionId"`
}

type BlobName struct {
	// The name of the blob.
	Content *string `xml:",chardata"`

	// Indicates if the blob name is encoded.
	Encoded *bool `xml:"Encoded,attr"`
}

type BlobPrefix struct {
	// REQUIRED
	Name *string `xml:"Name"`
}

// BlobProperties - Properties of a blob
type BlobProperties struct {
	// REQUIRED
	ETag *azcore.ETag `xml:"Etag"`

	// REQUIRED
	LastModified         *time.Time     `xml:"Last-Modified"`
	AccessTier           *AccessTier    `xml:"AccessTier"`
	AccessTierChangeTime *time.Time     `xml:"AccessTierChangeTime"`
	AccessTierInferred   *bool          `xml:"AccessTierInferred"`
	ArchiveStatus        *ArchiveStatus `xml:"ArchiveStatus"`
	BlobSequenceNumber   *int64         `xml:"x-ms-blob-sequence-number"`
	BlobType             *BlobType      `xml:"BlobType"`
	CacheControl         *string        `xml:"Cache-Control"`
	ContentDisposition   *string        `xml:"Content-Disposition"`
	ContentEncoding      *string        `xml:"Content-Encoding"`
	ContentLanguage      *string        `xml:"Content-Language"`

	// Size in bytes
	ContentLength             *int64          `xml:"Content-Length"`
	ContentMD5                []byte          `xml:"Content-MD5"`
	ContentType               *string         `xml:"Content-Type"`
	CopyCompletionTime        *time.Time      `xml:"CopyCompletionTime"`
	CopyID                    *string         `xml:"CopyId"`
	CopyProgress              *string         `xml:"CopyProgress"`
	CopySource                *string         `xml:"CopySource"`
	CopyStatus                *CopyStatusType `xml:"CopyStatus"`
	CopyStatusDescription     *string         `xml:"CopyStatusDescription"`
	CreationTime              *time.Time      `xml:"Creation-Time"`
	CustomerProvidedKeySHA256 *string         `xml:"CustomerProvidedKeySha256"`
	DeletedTime               *time.Time      `xml:"DeletedTime"`
	DestinationSnapshot       *string         `xml:"DestinationSnapshot"`

	// The name of the encryption scope under which the blob is encrypted.
	EncryptionScope             *string                 `xml:"EncryptionScope"`
	ExpiresOn                   *time.Time              `xml:"Expiry-Time"`
	ImmutabilityPolicyExpiresOn *time.Time              `xml:"ImmutabilityPolicyUntilDate"`
	ImmutabilityPolicyMode      *ImmutabilityPolicyMode `xml:"ImmutabilityPolicyMode"`
	IncrementalCopy             *bool                   `xml:"IncrementalCopy"`
	IsSealed                    *bool                   `xml:"Sealed"`
	LastAccessedOn              *time.Time              `xml:"LastAccessTime"`
	LeaseDuration               *LeaseDurationType      `xml:"LeaseDuration"`
	LeaseState                  *LeaseStateType         `xml:"LeaseState"`
	LeaseStatus                 *LeaseStatusType        `xml:"LeaseStatus"`
	LegalHold                   *bool                   `xml:"LegalHold"`

	// If an object is in rehydrate pending state then this header is returned with priority of rehydrate. Valid values are High
	// and Standard.
	RehydratePriority      *RehydratePriority `xml:"RehydratePriority"`
	RemainingRetentionDays *int32             `xml:"RemainingRetentionDays"`
	ServerEncrypted        *bool              `xml:"ServerEncrypted"`
	TagCount               *int32             `xml:"TagCount"`
}

type BlobTag struct {
	// REQUIRED
	Key *string `xml:"Key"`

	// REQUIRED
	Value *string `xml:"Value"`
}

// BlobTags - Blob tags
type BlobTags struct {
	// REQUIRED
	BlobTagSet []*BlobTag `xml:"TagSet>Tag"`
}

// Block - Represents a single block in a block blob. It describes the block's ID and size.
type Block struct {
	// REQUIRED; The base64 encoded block ID.
	Name *string `xml:"Name"`

	// REQUIRED; The block size in bytes.
	Size *int64 `xml:"Size"`
}

type BlockList struct {
	CommittedBlocks   []*Block `xml:"CommittedBlocks>Block"`
	UncommittedBlocks []*Block `xml:"UncommittedBlocks>Block"`
}

type BlockLookupList struct {
	Committed   []*string `xml:"Committed"`
	Latest      []*string `xml:"Latest"`
	Uncommitted []*string `xml:"Uncommitted"`
}

type ClearRange struct {
	// REQUIRED
	End *int64 `xml:"End"`

	// REQUIRED
	Start *int64 `xml:"Start"`
}

// ContainerItem - An Azure Storage container
type ContainerItem struct {
	// REQUIRED
	Name *string `xml:"Name"`

	// REQUIRED; Properties of a container
	Properties *ContainerProperties `xml:"Properties"`
	Deleted    *bool                `xml:"Deleted"`

	// Dictionary of
	Metadata map[string]*string `xml:"Metadata"`
	Version  *string            `xml:"Version"`
}

// ContainerProperties - Properties of a container
type ContainerProperties struct {
	// REQUIRED
	ETag *azcore.ETag `xml:"Etag"`

	// REQUIRED
	LastModified           *time.Time `xml:"Last-Modified"`
	DefaultEncryptionScope *string    `xml:"DefaultEncryptionScope"`
	DeletedTime            *time.Time `xml:"DeletedTime"`
	HasImmutabilityPolicy  *bool      `xml:"HasImmutabilityPolicy"`
	HasLegalHold           *bool      `xml:"HasLegalHold"`

	// Indicates if version level worm is enabled on this container.
	IsImmutableStorageWithVersioningEnabled *bool              `xml:"ImmutableStorageWithVersioningEnabled"`
	LeaseDuration                           *LeaseDurationType `xml:"LeaseDuration"`
	LeaseState                              *LeaseStateType    `xml:"LeaseState"`
	LeaseStatus                             *LeaseStatusType   `xml:"LeaseStatus"`
	PreventEncryptionScopeOverride          *bool              `xml:"DenyEncryptionScopeOverride"`
	PublicAccess                            *PublicAccessType  `xml:"PublicAccess"`
	RemainingRetentionDays                  *int32             `xml:"RemainingRetentionDays"`
}

// CORSRule - CORS is an HTTP feature that enables a web application running under one domain to access resources in another
// domain. Web browsers implement a security restriction known as same-origin policy that
// prevents a web page from calling APIs in a different domain; CORS provides a secure way to allow one domain (the origin
// domain) to call APIs in another domain
type CORSRule struct {
	// REQUIRED; the request headers that the origin domain may specify on the CORS request.
	AllowedHeaders *string `xml:"AllowedHeaders"`

	// REQUIRED; The methods (HTTP request verbs) that the origin domain may use for a CORS request. (comma separated)
	AllowedMethods *string `xml:"AllowedMethods"`

	// REQUIRED; The origin domains that are permitted to make a request against the storage service via CORS. The origin domain
	// is the domain from which the request originates. Note that the origin must be an exact
	// case-sensitive match with the origin that the user age sends to the service. You can also use the wildcard character '*'
	// to allow all origin domains to make requests via CORS.
	AllowedOrigins *string `xml:"AllowedOrigins"`

	// REQUIRED; The response headers that may be sent in the response to the CORS request and exposed by the browser to the request
	// issuer
	ExposedHeaders *string `xml:"ExposedHeaders"`

	// REQUIRED; The maximum amount time that a browser should cache the preflight OPTIONS request.
	MaxAgeInSeconds *int32 `xml:"MaxAgeInSeconds"`
}

// DelimitedTextConfiguration - Groups the settings used for interpreting the blob data if the blob is delimited text formatted.
type DelimitedTextConfiguration struct {
	// The string used to separate columns.
	ColumnSeparator *string `xml:"ColumnSeparator"`

	// The string used as an escape character.
	EscapeChar *string `xml:"EscapeChar"`

	// The string used to quote a specific field.
	FieldQuote *string `xml:"FieldQuote"`

	// Represents whether the data has headers.
	HeadersPresent *bool `xml:"HasHeaders"`

	// The string used to separate records.
	RecordSeparator *string `xml:"RecordSeparator"`
}

// FilterBlobItem - Blob info from a Filter Blobs API call
type FilterBlobItem struct {
	// REQUIRED
	ContainerName *string `xml:"ContainerName"`

	// REQUIRED
	Name             *string `xml:"Name"`
	IsCurrentVersion *bool   `xml:"IsCurrentVersion"`

	// Blob tags
	Tags      *BlobTags `xml:"Tags"`
	VersionID *string   `xml:"VersionId"`
}

// FilterBlobSegment - The result of a Filter Blobs API call
type FilterBlobSegment struct {
	// REQUIRED
	Blobs []*FilterBlobItem `xml:"Blobs>Blob"`

	// REQUIRED
	ServiceEndpoint *string `xml:"ServiceEndpoint,attr"`

	// REQUIRED
	Where      *string `xml:"Where"`
	NextMarker *string `xml:"NextMarker"`
}

// GeoReplication - Geo-Replication information for the Secondary Storage Service
type GeoReplication struct {
	// REQUIRED; A GMT date/time value, to the second. All primary writes preceding this value are guaranteed to be available
	// for read operations at the secondary. Primary writes after this point in time may or may
	// not be available for reads.
	LastSyncTime *time.Time `xml:"LastSyncTime"`

	// REQUIRED; The status of the secondary location
	Status *BlobGeoReplicationStatus `xml:"Status"`
}

// JSONTextConfiguration - json text configuration
type JSONTextConfiguration struct {
	// The string used to separate records.
	RecordSeparator *string `xml:"RecordSeparator"`
}

// KeyInfo - Key information
type KeyInfo struct {
	// REQUIRED; The date-time the key expires in ISO 8601 UTC time
	Expiry *string `xml:"Expiry"`

	// REQUIRED; The date-time the key is active in ISO 8601 UTC time
	Start *string `xml:"Start"`
}

// ListBlobsFlatSegmentResponse - An enumeration of blobs
type ListBlobsFlatSegmentResponse struct {
	// REQUIRED
	ContainerName *string `xml:"ContainerName,attr"`

	// REQUIRED
	Segment *BlobFlatListSegment `xml:"Blobs"`

	// REQUIRED
	ServiceEndpoint *string `xml:"ServiceEndpoint,attr"`
	Marker          *string `xml:"Marker"`
	MaxResults      *int32  `xml:"MaxResults"`
	NextMarker      *string `xml:"NextMarker"`
	Prefix          *string `xml:"Prefix"`
}

// ListBlobsHierarchySegmentResponse - An enumeration of blobs
type ListBlobsHierarchySegmentResponse struct {
	// REQUIRED
	ContainerName *string `xml:"ContainerName,attr"`

	// REQUIRED
	Segment *BlobHierarchyListSegment `xml:"Blobs"`

	// REQUIRED
	ServiceEndpoint *string `xml:"ServiceEndpoint,attr"`
	Delimiter       *string `xml:"Delimiter"`
	Marker          *string `xml:"Marker"`
	MaxResults      *int32  `xml:"MaxResults"`
	NextMarker      *string `xml:"NextMarker"`
	Prefix          *string `xml:"Prefix"`
}

// ListContainersSegmentResponse - An enumeration of containers
type ListContainersSegmentResponse struct {
	// REQUIRED
	ContainerItems []*ContainerItem `xml:"Containers>Container"`

	// REQUIRED
	ServiceEndpoint *string `xml:"ServiceEndpoint,attr"`
	Marker          *string `xml:"Marker"`
	MaxResults      *int32  `xml:"MaxResults"`
	NextMarker      *string `xml:"NextMarker"`
	Prefix          *string `xml:"Prefix"`
}

// Logging - Azure Analytics Logging settings.
type Logging struct {
	// REQUIRED; Indicates whether all delete requests should be logged.
	Delete *bool `xml:"Delete"`

	// REQUIRED; Indicates whether all read requests should be logged.
	Read *bool `xml:"Read"`

	// REQUIRED; the retention policy which determines how long the associated data should persist
	RetentionPolicy *RetentionPolicy `xml:"RetentionPolicy"`

	// REQUIRED; The version of Storage Analytics to configure.
	Version *string `xml:"Version"`

	// REQUIRED; Indicates whether all write requests should be logged.
	Write *bool `xml:"Write"`
}

// Metrics - a summary of request statistics grouped by API in hour or minute aggregates for blobs
type Metrics struct {
	// REQUIRED; Indicates whether metrics are enabled for the Blob service.
	Enabled *bool `xml:"Enabled"`

	// Indicates whether metrics should generate summary statistics for called API operations.
	IncludeAPIs *bool `xml:"IncludeAPIs"`

	// the retention policy which determines how long the associated data should persist
	RetentionPolicy *RetentionPolicy `xml:"RetentionPolicy"`

	// The version of Storage Analytics to configure.
	Version *string `xml:"Version"`
}

// PageList - the list of pages
type PageList struct {
	ClearRange []*ClearRange `xml:"ClearRange"`
	NextMarker *string       `xml:"NextMarker"`
	PageRange  []*PageRange  `xml:"PageRange"`
}

type PageRange struct {
	// REQUIRED
	End *int64 `xml:"End"`

	// REQUIRED
	Start *int64 `xml:"Start"`
}

type QueryFormat struct {
	// REQUIRED; The quick query format type.
	Type *QueryFormatType `xml:"Type"`

	// Groups the settings used for formatting the response if the response should be Arrow formatted.
	ArrowConfiguration *ArrowConfiguration `xml:"ArrowConfiguration"`

	// Groups the settings used for interpreting the blob data if the blob is delimited text formatted.
	DelimitedTextConfiguration *DelimitedTextConfiguration `xml:"DelimitedTextConfiguration"`

	// json text configuration
	JSONTextConfiguration *JSONTextConfiguration `xml:"JsonTextConfiguration"`

	// parquet configuration
	ParquetTextConfiguration any `xml:"ParquetTextConfiguration"`
}

// QueryRequest - Groups the set of query request settings.
type QueryRequest struct {
	// REQUIRED; The query expression in SQL. The maximum size of the query expression is 256KiB.
	Expression *string `xml:"Expression"`

	// CONSTANT; Required. The type of the provided query expression.
	// Field has constant value "SQL", any specified value is ignored.
	QueryType           *string             `xml:"QueryType"`
	InputSerialization  *QuerySerialization `xml:"InputSerialization"`
	OutputSerialization *QuerySerialization `xml:"OutputSerialization"`
}

type QuerySerialization struct {
	// REQUIRED
	Format *QueryFormat `xml:"Format"`
}

// RetentionPolicy - the retention policy which determines how long the associated data should persist
type RetentionPolicy struct {
	// REQUIRED; Indicates whether a retention policy is enabled for the storage service
	Enabled *bool `xml:"Enabled"`

	// Indicates whether permanent delete is allowed on this storage account.
	AllowPermanentDelete *bool `xml:"AllowPermanentDelete"`

	// Indicates the number of days that metrics or logging or soft-deleted data should be retained. All data older than this
	// value will be deleted
	Days *int32 `xml:"Days"`
}

// SignedIdentifier - signed identifier
type SignedIdentifier struct {
	// REQUIRED; An Access policy
	AccessPolicy *AccessPolicy `xml:"AccessPolicy"`

	// REQUIRED; a unique id
	ID *string `xml:"Id"`
}

// StaticWebsite - The properties that enable an account to host a static website
type StaticWebsite struct {
	// REQUIRED; Indicates whether this account is hosting a static website
	Enabled *bool `xml:"Enabled"`

	// Absolute path of the default index page
	DefaultIndexDocumentPath *string `xml:"DefaultIndexDocumentPath"`

	// The absolute path of the custom 404 page
	ErrorDocument404Path *string `xml:"ErrorDocument404Path"`

	// The default name of the index page under each directory
	IndexDocument *string `xml:"IndexDocument"`
}

type StorageError struct {
	Message *string
}

// StorageServiceProperties - Storage Service Properties.
type StorageServiceProperties struct {
	// The set of CORS rules.
	CORS []*CORSRule `xml:"Cors>CorsRule"`

	// The default version to use for requests to the Blob service if an incoming request's version is not specified. Possible
	// values include version 2008-10-27 and all more recent versions
	DefaultServiceVersion *string `xml:"DefaultServiceVersion"`

	// the retention policy which determines how long the associated data should persist
	DeleteRetentionPolicy *RetentionPolicy `xml:"DeleteRetentionPolicy"`

	// a summary of request statistics grouped by API in hour or minute aggregates for blobs
	HourMetrics *Metrics `xml:"HourMetrics"`

	// Azure Analytics Logging settings.
	Logging *Logging `xml:"Logging"`

	// a summary of request statistics grouped by API in hour or minute aggregates for blobs
	MinuteMetrics *Metrics `xml:"MinuteMetrics"`

	// The properties that enable an account to host a static website
	StaticWebsite *StaticWebsite `xml:"StaticWebsite"`
}

// StorageServiceStats - Stats for the storage service.
type StorageServiceStats struct {
	// Geo-Replication information for the Secondary Storage Service
	GeoReplication *GeoReplication `xml:"GeoReplication"`
}

// UserDelegationKey - A user delegation key
type UserDelegationKey struct {
	// REQUIRED; The date-time the key expires
	SignedExpiry *time.Time `xml:"SignedExpiry"`

	// REQUIRED; The Azure Active Directory object ID in GUID format.
	SignedOID *string `xml:"SignedOid"`

	// REQUIRED; Abbreviation of the Azure Storage service that accepts the key
	SignedService *string `xml:"SignedService"`

	// REQUIRED; The date-time the key is active
	SignedStart *time.Time `xml:"SignedStart"`

	// REQUIRED; The Azure Active Directory tenant ID in GUID format
	SignedTID *string `xml:"SignedTid"`

	// REQUIRED; The service version that created the key
	SignedVersion *string `xml:"SignedVersion"`

	// REQUIRED; The key as a base64 string
	Value *string `xml:"Value"`
}
