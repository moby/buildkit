# go-msgio - Message IO

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](https://protocol.ai)
[![](https://img.shields.io/badge/project-libp2p-yellow.svg?style=flat-square)](https://libp2p.io/)
[![](https://img.shields.io/badge/freenode-%23libp2p-yellow.svg?style=flat-square)](http://webchat.freenode.net/?channels=%23libp2p)
[![codecov](https://codecov.io/gh/libp2p/go-libp2p-netutil/branch/master/graph/badge.svg)](https://codecov.io/gh/libp2p/go-msgio)
[![Travis CI](https://travis-ci.org/libp2p/go-libp2p-netutil.svg?branch=master)](https://travis-ci.org/libp2p/go-msgio)
[![Discourse posts](https://img.shields.io/discourse/https/discuss.libp2p.io/posts.svg)](https://discuss.libp2p.io)

This is a simple package that helps read and write length-delimited slices. It's helpful for building wire protocols.

## Usage

### Reading

```go
import "github.com/libp2p/go-msgio"
rdr := ... // some reader from a wire
mrdr := msgio.NewReader(rdr)

for {
  msg, err := mrdr.ReadMsg()
  if err != nil {
    return err
  }

  doSomething(msg)
}
```

### Writing

```go
import "github.com/libp2p/go-msgio"
wtr := genReader()
mwtr := msgio.NewWriter(wtr)

for {
  msg := genMessage()
  err := mwtr.WriteMsg(msg)
  if err != nil {
    return err
  }
}
```

### Duplex

```go
import "github.com/libp2p/go-msgio"
rw := genReadWriter()
mrw := msgio.NewReadWriter(rw)

for {
  msg, err := mrdr.ReadMsg()
  if err != nil {
    return err
  }

  // echo it back :)
  err = mwtr.WriteMsg(msg)
  if err != nil {
    return err
  }
}
```

---

The last gx published version of this module was: 0.0.6: QmcxL9MDzSU5Mj1GcWZD8CXkAFuJXjdbjotZ93o371bKSf
