---
title: UndefinedVar
description: >-
  Variables should be defined before their use
aliases:
  - /go/dockerfile/rule/undefined-var/
---

## Output

```text
Usage of undefined variable '$foo'
```

## Description

This check ensures that environment variables and build arguments are correctly
declared before being used. While undeclared variables might not cause an
immediate build failure, they can lead to unexpected behavior or errors later
in the build process.

This check does not evaluate undefined variables for `RUN`, `CMD`, and
`ENTRYPOINT` instructions where you use the [shell form](https://docs.docker.com/reference/dockerfile/#shell-form).
That's because when you use shell form, variables are resolved by the command
shell.

It also detects common mistakes like typos in variable names. For example, in
the following Dockerfile:

```dockerfile
FROM alpine
ENV PATH=$PAHT:/app/bin
```

The check identifies that `$PAHT` is undefined and likely a typo for `$PATH`:

```text
Usage of undefined variable '$PAHT' (did you mean $PATH?)
```

## Examples

❌ Bad: `$foo` is an undefined build argument.

```dockerfile
FROM alpine AS base
COPY $foo .
```

✅ Good: declaring `foo` as a build argument before attempting to access it.

```dockerfile
FROM alpine AS base
ARG foo
COPY $foo .
```

❌ Bad: `$foo` is undefined.

```dockerfile
FROM alpine AS base
ARG VERSION=$foo
```

✅ Good: the base image defines `$PYTHON_VERSION`

```dockerfile
FROM python AS base
ARG VERSION=$PYTHON_VERSION
```

