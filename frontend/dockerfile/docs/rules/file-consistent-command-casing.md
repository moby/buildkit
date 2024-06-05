---
title: FileConsistentCommandCasing
description: All commands within the Dockerfile should use the same casing (either upper or lower)
aliases:
  - /go/dockerfile/rule/file-consistent-command-casing/
---

## Output

Example warning:

```text
Command 'foo' should match the case of the command majority (uppercase)
```

## Description

Instructions within a Dockerfile should have consistent casing through out the
entire files. Instructions are not case-sensitive, but the convention is to use
uppercase for instruction keywords to make it easier to distinguish keywords
from arguments.

Whether you prefer instructions to be uppercase or lowercase, you should make
sure you use consistent casing to help improve readability of the Dockerfile.

## Examples

❌ Bad: mixed uppercase and lowercase.

```dockerfile
FROM alpine:latest AS builder
run apk --no-cache add build-base

FROM builder AS build1
copy source1.cpp source.cpp
```

✅ Good: all uppercase.

```dockerfile
FROM alpine:latest AS builder
RUN apk --no-cache add build-base

FROM builder AS build1
COPY source1.cpp source.cpp
```

✅ Good: all lowercase.

```dockerfile
from alpine:latest as builder
run apk --no-cache add build-base

from builder as build1
copy source1.cpp source.cpp
```

## Related errors

- [`FromAsCasing`](./from-as-casing.md)

