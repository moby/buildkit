## Output

```text
Comment for build stage or argument should follow the format: `# <arg/stage name> <description>`.
```

## Description

The [`--call=outline`](https://docs.docker.com/reference/cli/docker/buildx/build/#call-outline)
and [`--call=targets`](https://docs.docker.com/reference/cli/docker/buildx/build/#call-outline)
flags for the `docker build` command print descriptions for build targets and arguments.
The descriptions are generated from [Dockerfile comments](https://docs.docker.com/reference/cli/docker/buildx/build/#descriptions)
that immediately precede the `FROM` or `ARG` instruction
and that begin with the name of the build stage or argument.
For example:

```dockerfile
# build-cli builds the CLI binary
FROM alpine AS build-cli
# VERSION controls the version of the program
ARG VERSION=1
```

Empty comment lines (`#`) separate paragraphs within the same comment block.
They do not detach the block from the instruction. A description can appear
before additional paragraphs or annotations in that block. Use a blank line
to separate comments that are not intended to describe the instruction.

## Examples

❌ Bad: A non-descriptive comment on the line preceding the `FROM` command.

```dockerfile
# a non-descriptive comment
FROM scratch AS base

# another non-descriptive comment
ARG VERSION=1
```

✅ Good: A description followed by additional paragraphs and annotations.

```dockerfile
# VERSION controls the version of the program
#
# Keep this version in sync with the release configuration.
#
# renovate: datasource=docker depName=example
ARG VERSION=1
```

✅ Good: Comments describing `ARG` keys and stages immediately proceeding the command.

```dockerfile
# base is a stage for compiling source
FROM scratch AS base
# VERSION This is the version number.
ARG VERSION=1
```
