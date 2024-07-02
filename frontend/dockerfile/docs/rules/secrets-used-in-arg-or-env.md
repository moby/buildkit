---
title: SecretsUsedInArgOrEnv
description: Potentially sensitive data should not be used in the ARG or ENV commands
aliases:
  - /go/dockerfile/rule/secrets-used-in-arg-or-env/
---

## Output

```text
Potentially sensitive data should not be used in the ARG or ENV commands
```

## Description

While it is common in many local development setups to pass secrets to running
processes through environment variables, setting these within a Dockerfile via
the `ENV` command means that these secrets will be committed to the build
history of the resulting image, exposing the secret. For the same reasons,
passing secrets in as build arguments, via the `ARG` command, will similarly
expose the secret. This rule reports violations where `ENV` and `ARG` key names
appear to be secret-related.

## Examples

❌ Bad: `AWS_SECRET_ACCESS_KEY` is a secret value.

```dockerfile
FROM scratch
ARG AWS_SECRET_ACCESS_KEY
```

