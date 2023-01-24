Go Netroute
===

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](http://protocol.ai)
[![](https://img.shields.io/badge/project-libp2p-yellow.svg?style=flat-square)](http://github.com/libp2p/libp2p)
[![Build Status](https://travis-ci.com/libp2p/go-netroute.svg?branch=master)](https://travis-ci.com/libp2p/go-netroute)

A cross-platform implementation of the [`gopacket/routing.Router`](https://godoc.org/github.com/google/gopacket/routing#Router) interface.

This library is derived from `gopacket` for linux, `x/net/route` for mac, and `iphlpapi.dll` for windows.

## Table of Contents

- [Install](#install)
- [Usage](#usage)
- [Documentation](#documentation)
- [Contribute](#contribute)
- [License](#license)

## Install

```
go get github.com/libp2p/go-netroute
```

## Usage

To be used for querying the local OS routing table.

```go
import (
    netroute "github.com/libp2p/go-netroute"
)

func main() {
    r, err := netroute.New()
    if err != nil {
        panic(err)
    }
    iface, gw, src, err := r.Route(net.IPv4(127, 0, 0, 1))
    fmt.Printf("%v, %v, %v, %v\n", iface, gw, src, err)
}
```

## Documentation

See the [gopacket](https://github.com/google/gopacket/blob/master/routing/) interface for thoughts on design,
and [godoc](https://godoc.org/github.com/libp2p/go-netroute) for API documentation.

## Contribute

Contributions welcome. Please check out [the issues](https://github.com/libp2p/go-netroute/issues).

Check out our [contributing document](https://github.com/libp2p/community/blob/master/contributing.md) for more information on how we work, and about contributing in general. Please be aware that all interactions related to multiformats are subject to the IPFS [Code of Conduct](https://github.com/ipfs/community/blob/master/code-of-conduct.md).

Small note: If editing the README, please conform to the [standard-readme](https://github.com/RichardLitt/standard-readme) specification.

## License

[BSD](LICENSE) © Will Scott, and the Gopacket authors (i.e. Google)
