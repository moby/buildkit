---
title: MultipleInstructionsDisallowed
description: Multiple instructions of the same type should not be used in the same stage
aliases:
  - /go/dockerfile/rule/multiple-instructions-disallowed/
---

## Output

```text
Multiple CMD instructions should not be used in the same stage because only the last one will be used
```

## Description

If you have multiple `CMD`, `HEALTHCHECK`, or `ENTRYPOINT` instructions in your
Dockerfile, only the last occurrence is used. An image can only ever have one
`CMD`, `HEALTHCHECK`, and `ENTRYPOINT`.

## Examples

❌ Bad: Duplicate instructions.

```dockerfile
FROM alpine
CMD echo "Hello, Norway!"
CMD echo "Hello, Sweden!"
# Only "Hello, Sweden!" will be printed
```

✅ Good: only one `CMD` instruction.

```dockerfile
FROM alpine
CMD echo "Hello, Norway!"; echo "Hello, Sweden!"
```

You can have both a regular, top-level `CMD`
and a separate `CMD` for a `HEALTHCHECK` instruction.

✅ Good: only one top-level `CMD` instruction.

```dockerfile
FROM python:alpine
RUN apk add curl
HEALTHCHECK --interval=1s --timeout=3s \
  CMD curl -f http://localhost:8080 || exit 1
CMD python -m http.server 8080
```

