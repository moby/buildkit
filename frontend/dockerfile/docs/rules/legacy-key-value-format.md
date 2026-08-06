---
title: LegacyKeyValueFormat
description: >-
  Legacy key/value format with whitespace separator should not be used
aliases:
  - /go/dockerfile/rule/legacy-key-value-format/
---

## Output

```text
"ENV key=value" should be used instead of legacy "ENV key value" format
```

## Description

The correct format for declaring environment variables and build arguments in a
Dockerfile is `ENV key=value` and `ARG key=value`, where the variable name
(`key`) and value (`value`) are separated by an equals sign (`=`).
Historically, Dockerfiles have also supported a space separator between the key
and the value (for example, `ARG key value`). This legacy format is deprecated,
and you should only use the format with the equals sign.

## Examples

❌ Bad: using a space separator for variable key and value.

```dockerfile
FROM alpine
ARG foo bar
```

✅ Good: use an equals sign to separate key and value.

```dockerfile
FROM alpine
ARG foo=bar
```

❌ Bad: multi-line variable declaration with a space separator.

```dockerfile
ENV DEPS \
    curl \
    git \
    make
```

✅ Good: use an equals sign and wrap the value in quotes.

```dockerfile
ENV DEPS="\
    curl \
    git \
    make"
```

## "Breaking" changes

Be aware that when converting multi-line legacy syntax to the modern
`key=value` format the output may be different. Review the changes carefully.

There are currently 2 known pitfalls regarding leading whitespaces and quote escape



### Leading whitespace

```dockerfile
ENV DEPS \
    curl \
    git \
    make
```

Should be written as:

```dockerfile
ENV DEPS="curl \
    git \
    make"
```
```dockerfile
ENV DEPS="\
curl \
    git \
    make"
```

### quote escape

```dockerfile
ENV SCRIPT1 script.sh --host=''
ENV SCRIPT2 script.sh --host 'argument'
ENV SCRIPT3 export MYFLAGS="\"flag\""
```

Should be written as:

```dockerfile
ENV SCRIPT1="script.sh --host="
ENV SCRIPT1="script.sh --host argument"
ENV SCRIPT3="export MYFLAGS=\"flag\""
```
