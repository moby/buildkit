---
title: ExposeInvalidFormat
description: >-
  IP address and host-port mapping should not be used in EXPOSE instruction. This will become an error in a future release
aliases:
  - /go/dockerfile/rule/expose-invalid-format/
---

## Output

```text
EXPOSE instruction should not define an IP address or host-port mapping, found '127.0.0.1:80:80'
```

## Description

The [`EXPOSE`](https://docs.docker.com/reference/dockerfile/#expose) instruction
in a Dockerfile is used to indicate which ports the container listens on at
runtime. It should not include an IP address or host-port mapping, as this is
not the intended use of the `EXPOSE` instruction. Instead, it should only
specify the port number and optionally the protocol (TCP or UDP).

> [!IMPORTANT]
> This will become an error in a future release.

## Examples

❌ Bad: IP address and host-port mapping used.

```dockerfile
FROM alpine
EXPOSE 127.0.0.1:80:80
```

✅ Good: only the port number is specified.

```dockerfile
FROM alpine
EXPOSE 80
```

❌ Bad: Host-port mapping used.

```dockerfile
FROM alpine
EXPOSE 80:80
```

✅ Good: only the port number is specified.

```dockerfile
FROM alpine
EXPOSE 80
```

