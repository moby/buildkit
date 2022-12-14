## Gonso (Go-ns-o)

Gonso is a library to do safe Linux [namespace](https://man7.org/linux/man-pages/man7/namespaces.7.html) manipulation in Go.

### Why?

Linux namespaces are per-thread, but Go programs don't have direct access to threads.
Doing namespace manipulation in Go requires careful use of
`runtime.LockOSThread()` and `runtime.UnlockOSThread()`. Linux namespaces can
have special rules for how they interact with each other and when/how you can
join them.  Gonso tries to handle these things for you.

While you can definitely use gonso to help learn Linux namespaces with Go since it
helps take care of Go's quirks due to the threading model, you should take great
care when using gonso in real code.

### Usage

Gonso has a concept called a `Set` which is a collection of namespaces. When
you create a `Set` you specify which namespaces are part of that set. You can
then `Unshare` namespaces from that `Set` to create a new `Set` with the new
namespaces. When you have the set setup with the namespaces you want you call
`Do` and pass a function in, this will run the function in the `Set`'s
namespaces.

```go
current, _ := gonso.Current()
newSet, _ := current.Unshare(unix.CLONE_NEWNS)
newSet.Do(false, func() bool {
    // do stuff in the new mount namespace
})
```

When calling `Do` you have some control over if the thread that was used to run
the function should be restored to the original namespaces or dropped. You can
completely disable thread restoration by passing `false` to `Do`. You can also
do the same by returning `false` from the function you pass to `Do`. In either
case, `false` will cause Go to exit the thread after the goroutine is done and
the thread cannot be re-used.  When `true` is returned from the function *and* `true`
is passed to `Do` gonso will make a best effort to restore the thread to the
original namespaces. If there are any errors restoring the thread the thread
will be dropped. In some cases, as with CLONE_NEWNS, the thread cannot be
restored to the original namespaces and will be dropped.

The reason you may want to restore the thread is that creating new threads is
not free, which is why Go manages a pool of threads to schedule your goroutines
on. Of course restoring the thread is also not free, and once you have a
certain number of namespaces that need to be restored the cost of restoring the
thread can become greater. Gonso leaves this up to you to decide.

Some actions cannot be undone in a thread so you will still need to be careful
to not try to restore a thread that has had such changes applied. Gonso also
can't track if you have done other things to the thread that would make it
unsafe to restore. For the most part this shouldn't be a problem as the Linux
kernel is pretty good about return an error in these cases, but it is something
to be aware of.

Gonso never changes the current thread or goroutine, it always runs the
function in a new goroutine.