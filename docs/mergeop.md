# MergeOp Requirements
1. Support for an LLB Op that merges the results of other LLB ops. The result of merging input ops should look the same as if you had taken the layers of the snapshots backing each input op result and applied them all on top of one another in the order provided.
1. The output of MergeOp must be usable as an LLB result in the same way that outputs of SourceOp, FileOp and ExecOp are today. This allows MergeOp to work seamlessly with the rest of LLB.
1. MergeOp must opportunistically optimize out the copying of data on disk when the underlying host+snapshotter has support for doing so (such as via overlayfs). If such optimizations aren’t available, the implementation must have a fallback mode with behavior that is functionally identical even if less efficient.
   * When compared to copies specifically, `Merge(dest, Scratch().File(Copy(src, "/a", "/b")))` must have the same or better complexity than `dest.File(Copy(src, "/a, "/b"))`
1. File metadata relevant to reproducibility, such as mtime, should be retained when merging an input.
1. Merges of layers should honor deletions that are part of the layer definition.
   * In the case where a directory is deleted and then recreated, merge op should use the "explicit whiteout" model (as described in the OCI image spec) where the previous contents of the directory are given individual whiteouts (as opposed to the opaque whiteout model). This is discussed more in the appendix.
1. When exporting an image created by merging in a separate llb.State, identical layers are not re-exported, they are re-used from their merged source.
   * If pushing to a remote registry that already has a layer input to a merge, no new layer is pushed
   * If a merge input doesn't exist locally (it's lazy) but does exist in the remote being pushed to, it stays lazy
1. Cache invalidation of one merge input does not result in other independent merge inputs being invalidated

# **Use Cases**
MergeOp enables users to combine independent snapshots without necessarily having to copy data on disk or introduce
unneeded cache dependencies.

For example, right now if you have separate LLB states (for example, separate stages in a multi-stage Dockerfile build) 
whose artifacts need to be combined together into a single state, your only option is to copy them together, which 
results in wasted disk space and increased execution time. It also means that the order you combine them in is important
and if any of the "lower layer" states being combined changes, the copying of all the states being combined above it 
will be invalidated and thus need to be repeated, increasing the disk space + execution time waste even further.

With MergeOp, you will instead be able to combine states together without any actual copy of data on disk (provided the 
underlying snapshotter satisfies some requirements, as described later). All of the merged states remain independent,
so if one of them changes, that doesn't invalidate the cache of other states in the merge and require extra work
to recreate them.

This feature is most useful once combined with other ops, such as:
1. llb.Copy - this enables chains of copy statements to be modeled as a single merge of copy inputs, which significantly improves the ability to reuse cache as described above.
1. DiffOp (described more in a later doc) - The proposed DiffOp will allow you to select which individual layers of an LLB state you want to merge. For instance, you can extract out just the changes made by a single Exec and then efficient apply them on top of different states.

# LLB
In terms of the LLB go client, this design proposes a single new function in the client/llb package:
* `Merge(inputs []State, opts ...ConstraintsOpt) State`

When merging states, the order matters, with "higher layer" states overriding and taking precedence over lower ones.
* The order of arguments is lower->higher, which is mostly arbitrary in the same way big vs. little endian is, but was chosen because it’s most intuitive and allows you to add layers to the stack by appending to the slice.

Merge will only merge together the root (i.e. "/") of the provided states.
* If a merge of subdirs of states is desired, you can use LLB like `Merge(base, llb.Scratch().File(llb.Copy(src, "/foo", "/bar"))).
* For the first implementation, this way of supporting selectors means that a copy will actually take place on disk. However, the caching benefits of MergeOp remain and we can also optimize this behavior more in the future (see Appendix).

# Proto
The proposed additions to the LLB proto are:

```
message MergeInput {
	int64 input = 1;
}
message MergeOp {
	repeated MergeInput inputs = 1;
}
```

The structure of MergeOp on the proto level is similar to that of ExecOp, just much simpler since there are less options. MergeInput is comparable to ExecOp’s Mount type; it’s just a reference to an Op.Input, which indexes an input state that should be merged. The MergeOp message type itself is currently just a list of those inputs as no other options are needed for now.

Given their similar structure, MergeOp should be forward compatible in the same way ExecOp is forward compatible today.

# Solver
The solver package will need updates to support the new MergeOp type, namely in solver/llbsolver/ops/. The two methods it will need to implement are:
1. CacheMap - this will be like a simpler version of ExecOp’s CacheMap method where the cache key is based on the digest of the MergeOp proto and the content checksums of each input to the Merge
2. Exec - this will just need to use cache.Manager to merge the input refs together (using a new API described in the next sections) and return that ImmutableRef as a Result.

# Cache Modifications
The cache package needs to be modified to support merging ImmutableRefs and to support creating MutableRefs on top of those merged ImmutableRefs. The overall goal here is to make this change as transparent to the rest of the codebase as possible so all existing code that currently uses refs today doesn’t have to be aware of the existence of merged refs in order to use them. This design thus proposes that we retain the existing ImmutableRef and MutableRef interfaces in the cache package, but update their underlying implementation to handle ref merges.

The only significant interface change will be extending cache.Accessor with this method:
* `Merge(ctx context.Context, parents []ImmutableRef, opts ...RefOption) (ImmutableRef, error)`

Merge, as you’d expect, creates an ImmutableRef that represents the merge of the given parents and returns it to the caller. That ImmutableRef can then be used just like any other ImmutableRef can be today.

To create a MutableRef on top of a merged ref (i.e. an ExecOp on top of a MergeOp), callers just need to use the existing CacheManager.New method and set the "parent" arg to a ref returned by a previous call to cache manager’s Merge method.

Internally, the implementation in the cache package has a few significant points:
1. `cacheRecord`s will now have two possible types of parents, either `layerParent` or `mergeParents`. These fields are mutually exclusive; only at most one of them should be non-nil.
   * `layerParent` is the same as the field that exists today, except it has been renamed from `parent`->`layerParent`. It represents that the cache record points to a snapshot whose parent is the given ref.
   * `mergeParents` is a slice of immutable refs that represents this ref is a merge of those parent refs. We'll call a ref with this set non-nil to be a "merge ref".
1. When creating the mountable for a merge ref, a new snapshot method (also called `Merge`) will be used, as described more below. The cache package won't directly contain code for implementing the creation of merged snapshots, it just calls out to the snapshot package and tells it to create a merged snapshot with a given snapshot ID, which it stores and then uses the same way as with any other type of ref.
1. The `Merge` method will:
   1. Be lazy in that the snapshot representing the merge of the inputs is not created until a mount for it has been requested.
   1. Flatten merges so that `Merge(A, Merge(B, C))` becomes `Merge(A, B, C)`. This just requires looking to see if an input is a merge ref and using its parents as inputs instead of using the merge ref directly.
   1. Finalize all input refs, so if any are immutable refs w/ an `equalMutable`, they will be finalized and can no longer be returned to mutable. It may be possible to lift this restriction eventually, but is not needed for the first iteration.
1. When exporting a merge ref, blobs will not be created for the ref directly. Instead, `computeBlobChain` will recurse to the merge refs inputs and compute their blobs. `GetRemote` will then behave similarly, with each merge ref getting "expanded" out into the joined chains of its inputs' blobs.

# Merge Snapshot Implementation
We will wrap the existing Snapshotter interface in Buildkit with an extra method:
* `Merge(ctx context.Context, key string, parents []string, opts ...snapshots.Opt) error`

This method takes a slice of parent keys and creates a new snapshot (identified by `key`) that, when mounted, will show the merged contents of those parent keys. The created snapshot will be committed, so it is read-only.

For the first iteration, merges will be implemented by hardlinking input snapshot contents into a new merged snapshot, with a fallback to copy files in case hardlinking is not supported by the underlying snapshotter. Further optimizations are possible for certain cases (discussed in appendix), but the hardlink w/ fallback to copy approach is chosen as the first baseline implementation because it is:
1. Highly efficient in the hardlink case and no worse than copy-based solutions in the fallback case.
1. Universal across supported snapshotters and the contents of the merged inputs

The high-level approach of creating a merged snapshot is:
1. Create a new active snapshot and obtain its mounts. From those mounts, the directory onto which merged files+directories will be applied is obtained (call this the `applyDir`).
   * How `applyDir` is obtained depends on the mount type:
      * Overlay-based (i.e. overlay, stargz) - it will extract out the `upperdir`.
      * Bind  (i.e. native) - it will extract out the `Source` field.
      * Other (i.e. `btrfs` maybe) - it will just mount to a tmpdir and use that mount as the `applyDir`
   * For the overlay-based snapshotter, the new active snapshot will be made on top of the bottom most merge input. That bottom most merge input will thus be skipped in the subsequent steps as it will already be "merged" so to speak. For other snapshotter, the active snapshot will be brand new (which allows you to skip e.g. copying the base input in the case of the native snapshotter).
1. Obtain read-only mounts for the current merge input ref and use those to apply the input contents to the `applyDir`.
   * A differ implementation is used to calculate the contents of each layer of the merge input bottom-up and apply the contents by recreating directory objects and hardlinking in file objects (with a fallback to copy in case hardlinks aren't supported for the snapshotter type or the filesystems underlying the snapshotter storage)
   * For overlay-based snapshotters, this will use the overlay-optimized differ which just reads changes directly from lowerdirs.
   * For other snapshotters, this will use the normal double-walking differ.
   * The use of the differ is important for two reasons:
      1. It ensures that deletions of files are included when applying layers, which in turn ensures that the deletions apply across independent merge inputs.
      1. By using the same differ as is used during export, we ensure that the merged snapshot has the same view as if it was exported as a blobchain re-imported and then mounted again. An example of this being important is discussed in the appendix section "Opaque Dirs".
   * See the "Performance of Merge vs Copy" section in the appendix for more possible optimizations of this process
1. Repeat the above step for each merge input.
1. Commit the active snapshot

# Examples
## Basic Example

```
stateA := Scratch().
   	 File(Mkfile("/foo", 0777, []byte("A"))).
   	 File(Mkfile("/a", 0777, []byte("A")))
stateB := Scratch().
   	 File(Mkfile("/foo", 0777, []byte("B"))).
   	 File(Mkfile("/b", 0777, []byte("B")))
mergedState := Merge(stateA, stateB) /*
   	 /a   - contains A
   	 /b   - contains B
   	 /foo - contains B
*/
mergedState = Merge(stateB, stateA) /*
   	 /a   - contains A
   	 /b   - contains B
   	 /foo - contains A
*/
```

As you can see, the contents of /foo depend on the order of the merge, with the higher state taking precedence.

/a and /b on the other hand are exclusive to stateA and stateB and thus get merged independently.

## Optimized Copy and Improved Caching Example
Say you start out with this LLB:
```
build1 := Image("builder").Run(...).Root()
result := Image("ubuntu:latest").File(Copy(build1, "/out", "/usr/bin"))
```
and now want to optimize it with MergeOp. You can transform it into this:
```
build1 := Image("builder").Run(...).Root()
result := Merge([]State{Image("ubuntu:latest"), Scratch().File(Copy(build1, "/out", "/usr/bin"))})
```

Say you are exporting `result` to a remote registry. While the MergeOp version will not optimize out the fact that the copy has to be performed at least once, it does provide some significant benefits in terms of caching laziness:
1. Rebases without pulling base image
   * Because MergeOp is lazy, the only input ever needed locally on disk is the copy input. If `ubuntu` is a lazy blob and the image is being pushed to a registry already containing it, it can stay lazy and never be pulled locally.
1. Efficient cache re-use when one input changes
   * If `ubuntu` changes across builds (i.e. the `latest` tag is updated), `build1` will not need to be re-copied; the cache for `build1` would remain validated and be re-usable across any other merges with it as an input. If remote cache imports from a previous build are being used, then `build1` could remain a lazy blob, with only possibly `ubuntu` needing to be pulled if the updated version doesn't exist in the registry being pushed to.

## Deletion Example

```
stateA := Scratch().
   	 File(Mkfile("/foo", 0777, []byte("A"))).
   	 File(Mkfile("/a", 0777, []byte("A")))
stateB := stateA.
   	 File(Rm("/foo")).
   	 File(Mkfile("/b", 0777, []byte("B")))
stateC := Scratch().
   	 File(Mkfile("/foo", 0777, []byte("C"))).
   	 File(Mkfile("/c", 0777, []byte("C")))
mergedState := Merge(stateB, stateC) /*
   	 /a   - contains A
   	 /b   - contains B
   	 /c   - contains C
	 /foo - contains C
*/
mergedState = Merge(stateC, stateB) /*
   	 /a   - contains A
   	 /b   - contains B
   	 /c   - contains C
	 /foo - [doesn't exist!]
*/
```

Here you can see that deletion of a file is considered part of a layer merge in the same way the creation or overwrite of a file is. The same idea will apply to deletions of directories (though not "opaque" directories, see appendix).

This example also illustrates that because stateB was created on top of stateA, stateA’s layers will be included when doing the merge. To be more concrete:
* `Merge(stateB, stateC)` results in layers being ordered stateC->stateB->stateA
* `Merge(stateC, stateB)` results in layers being ordered stateB->stateA->stateC

It's also worth noting that if you try to delete a file which doesn't exist and that doesn't result in an error, that
will not result in a deletion actually being included as part of the layer. Only deletion of existing files can possibly be
included with the layer definition.

## Run Example

```
base := Image("ubuntu")
repos := Merge(
  Git("github.com/foo/foo.git", “main”),
  Git("github.com/foo/bar.git", “main”),
)
serviceA := base.
  Run(Shlex("make -C /src serviceA")).
  AddMount("/src", repos).
  Root()
serviceB := base.
  Run(Shlex("make -C /src serviceB")).
  AddMount("/src", repos).
  Root()
result := Merge(serviceB, serviceA).Run(Shlex("blah")).Root()
```

This example is partially just meant to illustrate how merged states can be used seamlessly as input mounts to ExecOps, as seen by mounting the repos state, and how they can be used as the base for ExecOps too, as can be seen with the `result` state.

However, it also illustrates what may be surprising behavior. Namely, when you break down the layers of `result`, it will look like this:
* `Run blah` -> `make -C /src serviceA` -> `ubuntu` -> `make -C /src serviceB` -> `ubuntu`

What’s perhaps unexpected is that base’s layers (the ubuntu image) end up twice in the merged layer chain and also potentially override `make -C /src serviceB` even though that ExecOp was created on top of base. The behavior many users are more likely to want in this scenario is:
* `Run blah` -> `make -C /src serviceA` -> `make -C /src serviceB` -> `ubuntu`

Essentially, in this particular case the more desirable behavior is probably something akin to a topological sort of unique States based on their dependencies.
However, forcing States to always be topologically sorted is somewhat "magical", harder to reason about at more complicated scales and limiting in that there could be cases where users actually don’t want the behavior for whatever reason.

Instead, this design proposes that llb.Merge just implements the more simple ordering of layers exactly as specified by the user and leaves higher-level libraries/tools to implement topological sorting of States by dependencies if they want. While not fully possible yet, this will be enabled by DiffOp and this example will be updated once DiffOp exists.

# Appendix
## Opaque Dirs
### The Problem
One subtle but important detail surrounding all this is the handling of opaque dirs. In overlay filesystems, an opaque dir is created when you delete an existing directory and then recreate it within a single layer (possibly also putting more contents in it). When using overlayfs, such directories are marked with an opaque xattr, which results in the kernel fs implementation making them block out any lower directories at the same path instead of the default behavior where they are merged with them.

This behavior is largely an optimization; the more straightforward but less efficient option would be to create whiteout files for each of the contents of the directory that existed before (call that the "explicit whiteout" format, as opposed to the "opaque whiteout" format). In general, these two approaches have the same end effect once mounted, they only differ in how they are represented on the filesystem underlying the overlay mount.

For other snapshotter types such as the copy-based native snapshotter, there's no similar concept to opaque directories; on the underlying filesystem, deletion and recreation of a directory is indistinguishable from leaving the directory in place and deleting all of its previous contents. There's way to determine that a directory should be opaque, so only the "explicit whiteout" format can really be represented by those snapshotters.

Due to the above, the container ecosystem seems to have settled on the approach of representing layers using the "explicit whiteout" format, with support for the "opaque whiteout" format being limited to backwards compatibility on import.
* Specifically, the OCI image spec does have specifications for both the explicit and opaque format, but it says that implementations ["SHOULD generate layers using _explicit whiteout_ files", even though they "MUST accept both" for backwards compatibility reasons]("https://github.com/opencontainers/image-spec/blob/5ad6f50d628370b78a4a8df8a041ae89a6f0dedc/layer.md#L321).
* This is reflected in containerd's archive pack and unpack code, where opaque whiteouts are honored when unpacking but are never created for new archives, where the explicit whiteout format is always used.

All of the above has the important implication for MergeOp that we cannot honor opaque settings on directories **between merge inputs**, we can only honor opaque settings within the layers of a single merge input.

To show why this is, consider a case where you have a snapshot `snap1` that consists of 2 layers:
1. Bottom layer - dir `/foo` and file `/foo/1`
1. Top layer - An **opaque** dir `/foo` and a new file `/foo/2` (i.e. this layer deleted `/foo` from the previous layer and then remade it and put `2` in it)

If you were to export `snap1` by itself, the exporter will convert to the explicit whiteout format and you'd end up with a blob layers like:
1. `/foo` and `/foo/1`
1. `/foo`, `/foo/2` and an explicit whiteout for `/foo/1`

Now, consider a case where you do `merge1 := Merge(snap2, snap1)`, with `snap2` consisting of a single layer:
1. dir `/foo` and file `/foo/base`

If we were to honor opaque directories between separate merge inputs, that would mean that `/foo`'s opacity from the top layer of `snap1` would block out `/foo` from `snap2` below it and thus the merge view would consist of just:
1. `/foo/2`

However, now say we go to export `merge1`. The exporter does not support creating opaque whiteouts, so the exporter would have to somehow be told to create different blobs for `snap1` than before, namely:
1. `/foo` and `/foo/1`
1. `/foo`, `/foo/2`, an explicit whiteout for `/foo/1`, ***and an explicit whiteout for `/foo/base`***

This is a big problem because now the contents of the blobs for `snap1` depend on which inputs were merged below it, which violates our requirements and removes the cache+blob reuse benefits of MergeOp.

### The Solution
Our solution to this issue is to have MergeOp follow the same behavior as the exporter and OCI image spec: treat layers as though they are using the "explicit whiteout" format rather than the "opaque whitout" format. In practice, this means that while we honor deletions between merge inputs, we do not honor opaque xattrs between inputs when using an overlay-based snapshotter, we instead treat opaque directories as though they had explicit whiteouts. So, using the example above, when you mount `merge1` you will see the following:
1. `/foo`, `/foo/base` and `/foo/2`, with `/foo`'s metadata set as found in `snap1`.

In the current hardlink-based implementation, we get this behavior for free by re-using the overlay-optimized differ code. [That code is also used for blob creation, so it has the desired behavior of converting opaque directories into a set of explicit deletions](https://github.com/moby/buildkit/blob/b2ff444122ff769c13375a8b969cb299a42a9562/cache/blobs_linux.go#L312-L320).

In general, it's expected that this behavior will work with the vast majority of use cases. For users that don't want overlapping directories from separate merge inputs to be merged, they can add a separate `File.Rm` step to remove the directories they don't want before the merge.

One other thing worth noting is the existence of "unnecessary opaques", which are directories that are set opaque when newly created (due to [an optimization added in v4.10 kernels](https://github.com/torvalds/linux/commit/97c684cc911060ba7f97c0925eaf842f159a39e8) that has subsequently [been removed in v5.15+](https://github.com/torvalds/linux/commit/1fc31aac96d7060ecee18124be6de18cb2268922#diff-e706bc8120ee59f10f7b3916b632ce4cdf216f0f0116e3b10a1a490208eaf35b)). These are slightly distinct from the other opaque directories, but also cause similar problems. They can, however, be handled in the exact same way as other opaques by just not honoring them between merge inputs.

## Independent MergeOp vs. Optimized FileOp
Instead of adding a new MergeOp, another possible approach was to modify the existing FileOp with new fields that enable efficient merging.

One theoretical advantage to that approach would be that backwards compatibility is potentially easier as frontends won't need to ensure the buildkit daemon supports merge-op before sending LLB containing it.
   * However, there's a pretty big caveat to this. Namely, due to the way caching works, it's required that merges involving selectors be implemented as two separate vertexes (first one that projects the files and second one that does the merge).
   * That means the only way to model merges w/ selectors is to generate 2 llb.Copy calls that are embedded in each other. You couldn't have a single `FileOp` vertex that does both the selector and the merge.
   * The above approach would be backwards compatible in that it would still be valid LLB for older buildkit daemons. However, it would not be backwards compatible in that it would result in 2 copies on those old daemons, which would be much less efficient than the previous implementation.

Overall, the requirement that merges with selectors be implemented as 2 separate vertexes gets rid of the advantages of combining merge op into file op. While we could do it, we'd have to require that selectors and all other optional fields are not set when doing an efficient merge copy, at which point it feels like you're basically implementing two separate ops anyways.

Keeping merge separate also has some advantages in terms of keeping the internal code complexity a bit more separated and modular.

## Performance of Merge vs. Copy
MergeOp provides at least some performance benefits over copy in all cases due to improved caching behavior (as described in the examples) and re-use of exported blobs. However, it's also worth comparing their performance in terms of just snapshot creation time (i.e. the amount of time it takes to create snapshots using just llb.Copy vs the time it takes to make them when using Merge).

This comparison depends heavily on the underlying snapshotter, so the different types are considered separately.

We'll use the following example for all of them though. Say you are starting with this LLB:
```
ubuntu := Image("ubuntu")

build1 := Image("builder").Run(...).Root()
build1Copy := ubuntu.File(Copy(build1, "/a", "/b"))

build2 := Image("builder").Run(...).Root()
build2Copy := build1Copy.File(Copy(build2, "/c", "/d"))

output := build2Copy.Run(Shlex("blah")).Root()
```
and now optimizing it with MergeOp to be this:
```
ubuntu := Image("ubuntu")

build1 := Image("builder").Run(...).Root()
build1Copy := Scratch().File(Copy(build1, "/a", "/b"))

build2 := Image("builder").Run(...).Root()
build2Copy := Scratch().File(Copy(build2, "/c", "/d"))

output := Merge([]State{ubuntu, build1Copy, build2Copy}).Run(Shlex("blah")).Root()
```
And say for both cases your build is exporting the `output` state.

### Native Snapshotter
For the native snapshotter case (remember that the native snapshotter implements layers by copying all the contents of the parent to a child):
1. This is what happens today, without optimized merge:
   1. `build1Copy`
      1. The snapshotter copies the contents of `ubuntu` to initialize the new layer
      1. `build1` gets copied as specified to that layer
      1. So the overall work is:
         * `copy(ubuntu, /, /) + copy(build1, /a, /b)`
   1. `build2Copy`
      1. the snapshotter has to copy the previous snapshot copy to initialize the new layer
      1. `build2` gets copied as specified to that layer
      1. So the overall work is:
         * `copy(prevLayer1, /, /) + copy(build2, /c, /d)`
   1. `output`
      1. the snapshotter has to copy the previous snapshot copy to initialize the new layer
      1. `blah` gets ran
      1. So the overall work is:
         * `copy(prevLayer2, /, /) + run(blah)`
   1. Summing up the total work done, including the copies inherent to the implementation of the native snapshotter:
      * `copy(ubuntu, /, /) + copy(build1, /a, /b) + copy(prevLayer1, /, /) + copy(build2, /c, /d) + copy(prevLayer2, /, /) + run(blah)`
1. With the new idea, this is what will happen:
   1. `build1Copy`
      1. Merge is lazy, so no work happens there, but copy is done as previously, so it is done right away.
      1. So the overall work is:
         * `copy(build1, /a, /b)`
   1. `build2Copy`
      1. Merge is lazy, so no work happens there, but copy is done as previously, so it is done right away.
      1. So the overall work is:
         * `copy(build2, /c, /d)`
   1. `output`
      1. The merge needs to be unlazied now, which requires creating a new empty snapshot and then hardlinking all the inputs in.
      1. So the overall work is:
         * `hardlink(ubuntu, /, /) + hardlink(build1, /a, /b) + hardlink(build2, /c, /d) + run(blah)`
   1. Summing up the total work done:
      * `copy(build1, /a, /b) + copy(build2, /c, /d) + hardlink(ubuntu, /, /) + hardlink(build1, /a, /b) + hardlink(build2, /c, /d) + run(blah)`
1. Subtracting the amount of work required required for the new idea from the amount of work required today you get:
   * `copy(ubuntu, /, /)`
   * `+ copy(prevLayer1, /, /)`
   * `+ copy(prevLayer2, /, /)`
   * `- hardlink(ubuntu, /, /)`
   * `- hardlink(build1, /a, /b)` 
   * `- hardlink(build2, /c, /d)`

Hardlinking in these particular cases can never be slower than copying (and will very often be significantly faster), so it should be clear that the new approach is more performant than the previous one.

### Overlay Snapshotter
For the overlay snapshotter case:
1. This is what happens today:
   1. `build1Copy`
      1. The snapshotter creates a new layer on top of `ubuntu` and copies build1 into that as specified
      1. So the overall work is:
         * `copy(build1, /a, /b)`
   1. `build2Copy`
      1. The snapshotter creates a new layer on top of the previous and copies build2 into that as specified
      1. So the overall work is:
         * `copy(build2, /c, /d)`
   1. `output`
      1. the snapshotter creates a new empty layer on top of the previous
      1. So the overall work is:
         * `run(/blah)`
   1. Summing up the total work done:
      * `copy(build1, /a, /b) + copy(build2, /c, /d) + run(/blah)`
1. This is what happens w/ the merge optimization:
   1. `build1Copy`
      1. Merge is lazy, so no work happens there, but copy is done as previously, so it is done right away.
      1. So the overall work is:
         * `copy(build1, /a, /b)`
   1. `build2Copy`
      1. Merge is lazy, so no work happens there, but copy is done as previously, so it is done right away.
      1. So the overall work is:
         * `copy(build2, /c, /d)`
   1. `output`
      1. The merge needs to be unlazied now, which requires creating a new empty snapshot and then hardlinking all the inputs in.
      1. So the overall work is:
         * `hardlink(build1, /a, /b) + hardlink(build2, /c, /d) + run(blah)`
   1. Summing up the total work done:
      * `copy(build1, /a, /b) + copy(build2, /c, /d) + hardlink(build1, /a, /b) + hardlink(build2, /c, /d) + run(/blah)`

Overall, the amount of work done here is actually slightly larger w/ merge-op as compared with copy due to the need to create hardlinks during the merge. However:
   1. Once overlay-optimized merges are implemented (see the section below), the hardlinking steps will be skipped, meaning the before and after take the same amount of work.
   1. Both before and after overlay-optimized merges are implemented, the benefits in terms of caching will still be in place for the merge-op version, which can for many use cases significantly outweigh the costs of having to do some extra hardlinking.

## Future Extensions
### Overlay-optimized Merges
While the hardlink-based approach is quite fast and should be sufficient for most use cases, there are some further optimizations possible when using an overlay-based snapshotter that will:
1. Ensure that in the overlay snapshotter case, replacing merge op is **always** at least as fast as a set of copy ops in terms of total snapshot creation time (and usually much faster). See the above "Performance of Merge vs. Copy" section for details.
1. Possibly provide noticeable improvements to merges that have inputs with very large numbers of files and directories.

Namely, it is sometimes possible to create a merge snapshot by just joining together the lowerdirs and/or bind mounts of input snapshots rather than recreating them with hardlinks. This allows you skip running the differ (both the overlay-optimized differ and the double-walking one) and allows you to skip all the syscalls involved in walking the merge inputs, creating directories, creating hardlinks, etc.

The only case where this approach runs into issues is when one or more merge input has an opaque directory (see the "Opaque Dirs" section of the appendix above for why). In general, there's a few approaches for dealing with this:
1. If a merge sees that one of the non-base inputs has an opaque dir, fallback to just doing the hardlink merge
   * Note that many cases can still be optimized with this approach by avoiding the existence of opaque directories. For instance, the copy optimization usecase only creates merges like `Merge(a, Scratch().Copy(b, /foo, /bar))`, which means that all inputs besides the base one are single layer snapshots (due to being created on top of scratch) and thus can't possibly contain any opaque dirs.
   * Additionally, any newly imported images will not have this problem unless they were imported from legacy images that use the opaque whiteout format (which, as far as I can tell is only images that were created from AUFS snapshotters, though I'm not 100% sure).
1. Modify the snapshot w/ opaque directories to instead be represented by the explicit whiteout format. That is, remove the opaque xattr and add a bunch of whiteout devices to represent the deletion of all files in the snapshot chain's lower layers, all without making a hardlink copy of it.
   * One difficulty here is that this cannot be done "lazily", i.e. put off until you are about to use the snapshot as an input to a merge. That doesn't work because at the time of merge the snapshot could be in use as part of another mount, in which case any changes made to it would result in undefined behavior.

Overall, a hybrid of the two options above may be the best approach. Specifically, we can:
1. Start with just option 1 (and get some benefit right away).
2. Then, add an optional feature to Ops (or maybe a buildkitd config setting) to have newly created snapshots be converted on commit to use the explicit whiteout format rather than opaque whiteouts.
   * This gets us the end effect of option 2 but avoids the problem of modifying already mounted snapshots because it only happens on snapshot commit, at which time the snapshot needing fixup can't possibly be mounted.
   * It's proposed that this is enabled via an optional flag that defaults to false because it technically could result in a slight performance regression, though it's not expected to be noticeable outside snapshots with extremely large numbers of files and directories. If we decide the performance regression is small enough to be ignored, then we could instead just enable this behavior by default.

With the above in place, we can support overlay optimized merges while avoiding the issues with opaques as much as possible (and retain the still-fast hardlink approach as a fallback option for any other cases).

### Dockerfile COPY Integration
With MergeOp in place, we can update Dockerfile COPY statements to use MergeOp for more efficient caching behavior as described in the earlier copy examples. 

Namely, a Dockerfile like:
```
FROM ubuntu
COPY --from=foo /a /b
COPY --from=bar /c /d
```
Will be transformed as follows:
* `COPY --from=foo /a /b` becomes 
   * `merge1 := Merge(Image("ubuntu"), Scratch().Copy(foo, "/a", "/b"))`
* `COPY --from=bar /c /d` becomes
   * `merge2 := Merge(merge1, Scratch().Copy(bar, "/c", "/d"))`

All the benefits described in the previous sections and examples to caching will then apply to Dockerfile COPY statements.

### Moby Integration
The moby daemon embeds buildkit and then plugs in its own snapshotter that proxies calls to a graphdriver. This is overall not a problem becauase the implementation in Buildkit will work by taking a snapshotter implementation and then wrapping it with the `Merge` method, which will only require the existing snapshotter methods in order to work.

The only potential issue is that the buildkit code will enable/disable different optimizations based on the underlying snapshotter name (i.e. it will eventually enable overlay-specific optimizations for overlay and stargz), so we need to make sure that the name returned by moby's snapshotter's `Name()` method is handled by Buildkit if we want all possible optimizations to be enabled.