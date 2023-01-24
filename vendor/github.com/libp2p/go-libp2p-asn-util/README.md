# go-libp2p-asn-util
===

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](http://protocol.ai)
[![](https://img.shields.io/badge/project-libp2p-yellow.svg?style=flat-square)](http://github.com/libp2p/libp2p)

A library to lookup the ASN(Autonomous System Number) for an IP address. It uses the IPv6 to ASN database downloaded from https://iptoasn.com/.
Supports ONLY IPv6 addresses for now.   

## Table of Contents

- [Install](#install)
- [Usage](#usage)
- [Documentation](#documentation)
- [Contribute](#contribute)
- [License](#license)

## Install

```
go get github.com/libp2p/go-libp2p-asn-util
```

## Usage

```go
import (
    asn "github.com/libp2p/go-libp2p-asn-util"
)

func main() {
   store, err := asn.NewAsnStore()
   
   asNumber,err := store.AsnForIP(net.ParseIP("2a03:2880:f003:c07:face:b00c::2"))
 }
```

## Contribute

Feel free to join in. All welcome. Open an [issue](https://github.com/libp2p/go-libp2p-asn/issues)!

This repository falls under the IPFS [Code of Conduct](https://github.com/ipfs/community/blob/master/code-of-conduct.md).

## License

MIT

---