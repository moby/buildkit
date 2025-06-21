# Sources

BuildKit is capable of loading data from different sources, for example an OCI registry, a local directory, or a git repository. Each of these
is used as a "build context".

A specific layout of data in a specific source is described by a URI-style identifier, where the protocol specifies the source type, and the host and path specify how
to identify and locate the content in that type.

For example:

* `docker-image://docker.io/library/nginx:latest` uses type `containerimage` to indicate that it should look up the OCI image `library/nginx:latest` from the OCI registry at `docker.io`
* `path/to/content` uses the default protocol of local file, as indicated by no protocol, to indicate that it should find the contents at the local relative path `path/to/content`

This document describes how to create additional sources, i.e. locations from which buildkit can source
content. You can use these to add the ability to source images from, for example, ftp, scp, OCI layout, or any
other source of content.

## Source Responsibility

A source has a few responsibilities:

* Converting a unique source content reference into retrievable content. For example, the `containerimage` source knows how to find the data represented by `docker-image://docker.io/library/nginx:latest`.
* Creating a unique cache key for a unique source content reference. For example, the `containerimage` source knows how to create a unique key, in this case the root manifest hash, for  `docker-image://docker.io/library/nginx:latest`.
* Laying out the content onto a filesystem path and providing a mountpoint which the solver can use to mount the content. This is called creating a "snapshot".

## Steps

The overall steps are:

1. Add a new type identifier, or protocol name, for the source.
1. Create the new source in its own directory, compliant with the [Source interface](../source/manager.go#L13) and [SourceInstance interface](../source/manager.go#L18).
1. Create an [Identifier](../source/identifier.go#L31) for the new source, and add it to the `func FromString()` and `func FromLLB()` functions.
1. Register the new source in [NewWorker()](../worker/base/worker.go).

### Add a new protocol for the source

The source types, which also are the protocol names, are defined in [types.go](../source/types/types.go). Find a representative type string
and add it to the file, for example:

```go
const (
	DockerImageScheme = "docker-image"
	GitScheme         = "git"
	LocalScheme       = "local"
	HTTPScheme        = "http"
	HTTPSScheme       = "https"
	FTPScheme         = "ftp"   // <--- added here
)
```

### Create the new source

All of the sources are defined, each in its own directory, in [source](../source/). For example, the following is a partial listing of those that exist:

```
source/containerimage/
source/local/
source/http/
```

To add one for ftp, create the directory `source/ftp/`.

In that directory, create any files you need. However, the following must be exported as publicly available:

```go
func NewSource(opt Opt) (source.Source, error)
```

The above must return a `source.Source` interface, whose definition is [here](../source/manager.go#L13):

```go
type Source interface {
	ID() string
	Resolve(ctx context.Context, id Identifier, sm *session.Manager, vtx solver.Vertex) (SourceInstance, error)
}
```

The function `ID()` must return the type name created earlier in `types.go`.

The function `Resolve()` must return a `SourceInstance`, which is the implementation used to perform functions on a specific source identifier.
The `id` will give the unique identifier for the source to be retrieved. For example, if the source is `docker-image://docker.io/library/nginx:latest`,
then it will use the source registered to handle `docker-image`, and when `Resolve()` is called, it will be passed `docker.io/library/nginx:latest`
as the value of `id`.

The returned value from `Resolve()` must implement [SourceInstance](../source/manager.go#L18):

```go
type SourceInstance interface {
	CacheKey(ctx context.Context, g session.Group, index int) (string, string, solver.CacheOpts, bool, error)
	Snapshot(ctx context.Context, g session.Group) (cache.ImmutableRef, error)
}
```

A `SourceInstance` is returned by `Resolve()`, so it is assumed already to know what its identifier is, e.g. `docker.io/library/nginx:latest`.

The two functions in the signature of the `SourceInstance` are expected to perform the following functions.

#### `CacheKey`

`CacheKey()` gives the solver a key that uniquely identifies the instance of the operation (including its arguments and inputs).
So for a container image source, it would basically amount to the image digest, plus some other metadata.

The purpose of `CacheKey()` is for the solver to be able to ask the source, "I have this `id`, what unique cache key is there for it?"
If the response is identical to the one already in cache, the solver knows it does not need to retrieve the data again, and simply can
use what is in its cache. If it is different or does not exist, then it knows it needs to ask the source for the content referred to by the
`id`.

This is expected to be idempotent, a read-only activity that returns a unique cache key.

The arguments to `CacheKey()` are:

* `ctx context.Context`: A golang [context](https://pkg.go.dev/context#Context).
* `g session.Group`: The [session.Group](https://pkg.go.dev/github.com/moby/buildkit@v0.10.1/session#Group) contains parameters to identify the specific buildkit client calling this build. These may be useful for things like credentials.
* `index int`: The `CacheKey()` can be called multiple times for the same source item, each indicating that it is a separate subitem. The `index` indicates which iteration of call this is.

The expected return values are:

* `cacheKey string`: the unique cache ID; this might be the combination of a hash with the source identifier. For example, `docker-image` uses the hash of the image config, while `local` combines the path to the directory and hash of a json that includes the session and search parameters.
* `imgDigest string`: the digest of the root of the image. For example, `docker-image` uses the hash of the root manifest or index, while `local` returns the hash of the constructed json of the session and search parameters.
* `cacheOpts solver.CacheOpts`: The [CacheOpts](https://pkg.go.dev/github.com/moby/buildkit@v0.10.1/solver#CacheOpts) contain metadata that can be passed from one iteration of `CacheKey()` to another, enabling context to be passed from call to call without the `Source` needing to store information. This is useful for items like progress streams and remote reference information.
* `cacheDone bool`: Indicates whether or not caching is complete. If it is not, then the solver will call `CacheKey()` again, incrementing the index.
* `err error`: error, if any.

The `cacheDone` (returned from `CacheKey()`) and `index` (parameter passed to `CacheKey()`) enable the
`SourceInstance` to do caching in multiple parallel runs. If `CacheKey()` is not complete, it should
return `cacheDone = false`, which will cause the solver to call `CacheKey()` again, with an incremented
`index`. It is up to the `SourceInstance` implementation to determine when caching is done and, if so,
return `cacheDone = true`.

#### `Snapshot`

`Snapshot()` is what the solver calls to retrieve the content for the `id`. Normally, this gets run if it turns out there is no cached result. It does the work to
actually run the operation and create the underlying cache reference in the buildkit worker.

When `Snapshot()` is called, the `SourceInstance` is expected to:

1. Retrieve any content necessary and lay it out.
1. Create a mountpoint wherein the solver could mount the content.
1. Return the mountpoint reference, so that the solver will mount the content.

Note that it is not absolutely necessary to retrieve the entire content when `Snapshot()` is called. It is possible to do a "lazy retrieval". For example,
`docker-image` can use lazy retrieval, and defer pulling layers until they really are needed. However, everything that is needed to retrieve the content gets set up
by `Snapshot()`.

For example, `docker-image` pulls the image, while `local` ???

The arguments to `Snapshot()` are:

* `ctx context.Context`: golang [context](https://pkg.go.dev/context#Context)
* `g session.Group`: The [session.Group](https://pkg.go.dev/github.com/moby/buildkit@v0.10.1/session#Group) contains parameters to identify the specific buildkit client calling this build. These may be useful for things like credentials.

The expected return values are:

* `cache.ImmutableRef`: a [cache.ImmutableRef interface](../cache/refs.go#L48), which is used to read the actual content.
* `err error`: error, if any.

### Create a new Identifier

The source file [identifier.go](../source/identifier.go) defines an [Identifier interface](../source/identifier.go#L31):

```go
type Identifier interface {
	ID() string // until sources are in process this string comparison could be avoided
}
```

Each source needs to have an identifier, normally in this file, that implements `Identifier`. For example:

```go
type ImageIdentifier struct {
	Reference   reference.Spec
	Platform    *ocispecs.Platform
	ResolveMode ResolveMode
	RecordType  client.UsageRecordType
}

func NewImageIdentifier(str string) (*ImageIdentifier, error) {
	ref, err := reference.Parse(str)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if ref.Object == "" {
		return nil, errors.WithStack(reference.ErrObjectRequired)
	}
	return &ImageIdentifier{Reference: ref}, nil
}

func (*ImageIdentifier) ID() string {
	return srctypes.DockerImageScheme
}
```

You need to add your implementation to this file, whose `ID()` returns the correct type. For example, to add one for ftp:

```go
type FTPIdentifier struct {
	URL   url.URL
}

func NewFTPIdentifier(str string) (*FTPIdentifier, error) {
    ref, err := url.Parse(str)
    if err != nil {
        return nil, err
    }
	return &FTPIdentifier{URL: ref}, nil
}

func (*FTPIdentifier) ID() string {
	return srctypes.FTPScheme
}
```

Once your `Identifier` is created, you need to add it to `FromString()` and `FromLLB()` in the same file.
For example:

```go
func FromString(s string) (Identifier, error) {
	// TODO: improve this
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 {
		return nil, errors.Wrapf(errInvalid, "failed to parse %s", s)
	}

	switch parts[0] {
	case srctypes.DockerImageScheme:
		return NewImageIdentifier(parts[1])
	case srctypes.GitScheme:
		return NewGitIdentifier(parts[1])
	case srctypes.LocalScheme:
		return NewLocalIdentifier(parts[1])
	case srctypes.HTTPSScheme:
		return NewHTTPIdentifier(parts[1], true)
	case srctypes.HTTPScheme:
		return NewHTTPIdentifier(parts[1], false)
    // Added FTP here below
	case srctypes.FTPScheme:
		return NewFTPIdentifier(parts[1], false)
	default:
		return nil, errors.Wrapf(errNotFound, "unknown schema %s", parts[0])
	}
}
```

Each entry in `FromLLB()` checks the type and saves the attributes.

```go
	if id, ok := id.(*FTPIdentifier); ok {
		for k, v := range op.Source.Attrs {
			switch k {
                // handle key/value attributes
			}
		}
	}
```

### Register the Source

With your new `Source` in place, you need to register the `Source` with the worker, which processes source content and selects a `Source` to
retrieve them.

You register the `Source` in [worker.go](../worker/base/worker.go) in `NewWorker()`.
The function creates an instance for each `Source` type, and then registers it.

For example, the http source is registered as:

```go
	hs, err := http.NewSource(http.Opt{
		CacheAccessor: cm,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(hs)
```

Simply add your source and register it, for example:

```go
	fs, err := ftp.NewSource(ftp.Opt{
		CacheAccessor: cm,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(fs)
```