# go-multicodec

[![standard-readme compliant](https://img.shields.io/badge/readme%20style-standard-brightgreen.svg)](https://github.com/RichardLitt/standard-readme)

> Generated Go constants for the [multicodec table](https://github.com/multiformats/multicodec) used by the [multiformats](https://github.com/multiformats/multiformats) projects.

## Table of Contents

- [Install](#install)
- [Usage](#usage)
- [Generator](#generator)
- [Maintainers](#maintainers)
- [Contribute](#contribute)
- [License](#license)

## Install

`go-multicodec` is a standard Go module:

	go get github.com/multiformats/go-multicodec


## Type

`Code` describes an integer reserved in the multicodec table, defined at [multiformats/multicodec/table.csv](https://github.com/multiformats/multicodec/blob/master/table.csv).

```go
type Code uint64
```

## Usage

### Importing Code constant

```go
package main

import "github.com/multiformats/go-multicodec"

func main() {
	code := multicodec.Sha2_256 // Code
	name := multicodec.Sha2_256.String()
}
```

The corresponding `name` value for each codec from the [multicodecs table](https://raw.githubusercontent.com/multiformats/multicodec/master/table.csv) can be accessed via its `String` method. For example, `multicodec.Sha2_256.String()` will return `sha2-256`.

### Code from string

```go
var multicodec.Code code 
err := code.Set("libp2p-key")
```


### Code from uint64

```go
rawCode := multicodec.Code(0x55)
```

## Generator

To generate the constants yourself:

	git clone https://github.com/multiformats/go-multicodec
	cd go-multicodec
	git submodule init && git submodule update
	go generate

Note: You may need to install `stringer` via `go install golang.org/x/tools/cmd/stringer`.

## Maintainers

[@mvdan](https://github.com/mvdan).

## Contribute

Contributions welcome. Please check out [the issues](https://github.com/multiformats/go-multicodec/issues).

Check out our [contributing document](https://github.com/multiformats/multiformats/blob/master/contributing.md) for more information on how we work, and about contributing in general. Please be aware that all interactions related to multiformats are subject to the IPFS [Code of Conduct](https://github.com/ipfs/community/blob/master/code-of-conduct.md).

Small note: If editing the README, please conform to the [standard-readme](https://github.com/RichardLitt/standard-readme) specification.

## License

SPDX-License-Identifier: Apache-2.0 OR MIT
