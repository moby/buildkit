---
title: SecretsUsedInArgOrEnv
description: >-
  Sensitive data should not be used in the ARG or ENV commands
aliases:
  - /go/dockerfile/rule/secrets-used-in-arg-or-env/
---

## Output

```text
Potentially sensitive data should not be used in the ARG or ENV commands
```

## Description

While it is common to pass secrets to running processes
through environment variables during local development,
setting secrets in a Dockerfile using `ENV` or `ARG`
is insecure because they persist in the final image.
This rule reports violations where `ENV` and `ARG` keys
indicate that they contain sensitive data.

Instead of `ARG` or `ENV`, you should use secret mounts,
which expose secrets to your builds in a secure manner,
and do not persist in the final image or its metadata.
See [Build secrets](https://docs.docker.com/build/building/secrets/).

## Examples

❌ Bad: using ARG to pass AWS credentials.

```dockerfile
ARG AWS_ACCESS_KEY_ID
ARG AWS_SECRET_ACCESS_KEY
RUN aws s3 cp s3://my-bucket/file .
```

✅ Good: using secret mounts with environment variables.

```dockerfile
RUN --mount=type=secret,id=aws_key_id,env=AWS_ACCESS_KEY_ID \
    --mount=type=secret,id=aws_secret_key,env=AWS_SECRET_ACCESS_KEY \
    aws s3 cp s3://my-bucket/file .
```

To build with these secrets:

```console
$ docker buildx build \
    --secret id=aws_key_id,env=AWS_ACCESS_KEY_ID \
    --secret id=aws_secret_key,env=AWS_SECRET_ACCESS_KEY .
```

