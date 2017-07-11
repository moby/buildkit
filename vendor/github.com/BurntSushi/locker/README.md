Package locker is a simple package to manage named ReadWrite mutexes. These
appear to be especially useful for synchronizing access to session based
information in web applications.

The common use case is to use the package level functions, which use a package
level set of locks (safe to use from multiple goroutines simultaneously).
However, you may also create a new separate set of locks.

All locks are implemented with read-write mutexes. To use them like a regular
mutex, simply ignore the RLock/RUnlock functions.


### Installation

    go get github.com/BurntSushi/locker


### Documentation

http://godoc.org/github.com/BurntSushi/locker

