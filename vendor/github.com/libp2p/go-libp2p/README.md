
<h1 align="center">
  <a href="libp2p.io"><img width="250" src="https://github.com/libp2p/libp2p/blob/master/logo/black-bg-2.png?raw=true" alt="libp2p hex logo" /></a>
</h1>

<h3 align="center">The Go implementation of the libp2p Networking Stack.</h3>

<p align="center">
  <a href="http://protocol.ai"><img src="https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square" /></a>
  <a href="http://libp2p.io/"><img src="https://img.shields.io/badge/project-libp2p-yellow.svg?style=flat-square" /></a>
  <a href="https://pkg.go.dev/github.com/libp2p/go-libp2p"><img src="https://pkg.go.dev/badge/github.com/libp2p/go-libp2p.svg" alt="Go Reference"></a>
  <a href="https://discuss.libp2p.io"><img src="https://img.shields.io/discourse/https/discuss.libp2p.io/posts.svg"/></a>
</p>

# Table of Contents

- [Background](#background)
- [Usage](#usage)
  - [Examples](#examples)
- [Development](#development)
  - [Using the go-libp2p Workspace](#using-the-go-libp2p-workspace)
  - [Tests](#tests)
  - [Packages](#packages)
- [Contribute](#contribute)
- [Supported Go Versions](#supported-go-versions)

## Background

[libp2p](https://github.com/libp2p/specs) is a networking stack and library modularized out of [The IPFS Project](https://github.com/ipfs/ipfs), and bundled separately for other tools to use.
>
libp2p is the product of a long, and arduous quest of understanding -- a deep dive into the internet's network stack, and plentiful peer-to-peer protocols from the past. Building large-scale peer-to-peer systems has been complex and difficult in the last 15 years, and libp2p is a way to fix that. It is a "network stack" -- a protocol suite -- that cleanly separates concerns, and enables sophisticated applications to only use the protocols they absolutely need, without giving up interoperability and upgradeability. libp2p grew out of IPFS, but it is built so that lots of people can use it, for lots of different projects.

To learn more, check out the following resources:
- [**Our documentation**](https://docs.libp2p.io)
- [**Our community discussion forum**](https://discuss.libp2p.io)
- [**The libp2p Specification**](https://github.com/libp2p/specs)
- [**js-libp2p implementation**](https://github.com/libp2p/js-libp2p)
- [**rust-libp2p implementation**](https://github.com/libp2p/rust-libp2p)

## Usage

This repository (`go-libp2p`) serves as the entrypoint to the universe of packages that compose the Go implementation of the libp2p stack.

You can start using go-libp2p in your Go application simply by adding imports from our repos, e.g.:

```go
import "github.com/libp2p/go-libp2p"
```

### Examples

Examples can be found in the [examples folder](examples).

## Development

### Tests

`go test ./...` will run all tests in the repo.

# Contribute

go-libp2p is part of [The IPFS Project](https://github.com/ipfs/ipfs), and is MIT-licensed open source software. We welcome contributions big and small! Take a look at the [community contributing notes](https://github.com/ipfs/community/blob/master/CONTRIBUTING.md). Please make sure to check the [issues](https://github.com/ipfs/go-libp2p/issues). Search the closed ones before reporting things, and help us with the open ones.

Guidelines:

- read the [libp2p spec](https://github.com/libp2p/specs)
- ask questions or talk about things in  our [discussion forums](https://discuss.libp2p.io), or open an [issue](https://github.com/libp2p/go-libp2p/issues) for bug reports, or #libp2p on freenode.
- ensure you are able to contribute (no legal issues please -- we use the DCO)
- get in touch with @marten-seemann about how best to contribute
- have fun!

There's a few things you can do right now to help out:
 - Go through the modules below and **check out existing issues**. This would be especially useful for modules in active development. Some knowledge of IPFS/libp2p may be required, as well as the infrasture behind it - for instance, you may need to read up on p2p and more complex operations like muxing to be able to help technically.
 - **Perform code reviews**.
 - **Add tests**. There can never be enough tests.

## Supported Go Versions

We test against and support the two most recent major releases of Go. This is
informed by Go's own [security policy](https://go.dev/security).
