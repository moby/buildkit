# go-ipfs-api

[![](https://img.shields.io/badge/made%20by-Protocol%20Labs-blue.svg?style=flat-square)](https://protocol.ai)
[![](https://img.shields.io/badge/project-IPFS-blue.svg?style=flat-square)](https://ipfs.io/)
[![](https://img.shields.io/badge/matrix-%23ipfs-blue.svg?style=flat-square)](https://app.element.io/#/room/#ipfs:matrix.org)
[![standard-readme compliant](https://img.shields.io/badge/standard--readme-OK-green.svg?style=flat-square)](https://github.com/RichardLitt/standard-readme)
[![GoDoc](https://godoc.org/github.com/ipfs/go-ipfs-api?status.svg)](https://godoc.org/github.com/ipfs/go-ipfs-api)
[![Build Status](https://travis-ci.org/ipfs/go-ipfs-api.svg)](https://travis-ci.org/ipfs/go-ipfs-api) 

![](https://camo.githubusercontent.com/651f7045071c78042fec7f5b9f015e12589af6d5/68747470733a2f2f697066732e696f2f697066732f516d514a363850464d4464417367435a76413155567a7a6e3138617356636637485676434467706a695343417365)

> The go interface to ipfs's HTTP API

## Install

```sh
go get -u github.com/ipfs/go-ipfs-api
```

This will download the source into `$GOPATH/src/github.com/ipfs/go-ipfs-api`.

## Usage

See [the godocs](https://godoc.org/github.com/ipfs/go-ipfs-api) for details on available methods. This should match the specs at [ipfs/specs (Core API)](https://github.com/ipfs/specs/blob/master/API_CORE.md); however, there are still some methods which are not accounted for. If you would like to add any of them, see the contribute section below. See also the [HTTP API](https://docs.ipfs.io/reference/http/api/).

### Example

Add a file with the contents "hello world!":

```go
package main

import (
	"fmt"
	"strings"
    	"os"

    	shell "github.com/ipfs/go-ipfs-api"
)

func main() {
	// Where your local node is running on localhost:5001
	sh := shell.NewShell("localhost:5001")
	cid, err := sh.Add(strings.NewReader("hello world!"))
	if err != nil {
        fmt.Fprintf(os.Stderr, "error: %s", err)
        os.Exit(1)
	}
    fmt.Printf("added %s", cid)
}
```

For a more complete example, please see: https://github.com/ipfs/go-ipfs-api/blob/master/tests/main.go

## Contribute

Contributions are welcome! Please check out the [issues](https://github.com/ipfs/go-ipfs-api/issues).

### Want to hack on IPFS?

[![](https://cdn.rawgit.com/jbenet/contribute-ipfs-gif/master/img/contribute.gif)](https://github.com/ipfs/community/blob/master/CONTRIBUTING.md)

## License

MIT
