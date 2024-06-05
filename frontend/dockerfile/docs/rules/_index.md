---
title: Build checks
description: |
  BuildKit has built-in support for analyzing your build configuration based on
  a set of pre-defined rules for enforcing Dockerfile and building best
  practices.
keywords: buildkit, linting, dockerfile, frontend, rules
---

BuildKit has built-in support for analyzing your build configuration based on a
set of pre-defined rules for enforcing Dockerfile and building best practices.
Adhering to these rules helps avoid errors and ensures good readability of your
Dockerfile.

Checks run as a build invocation, but instead of producing a build output, it
performs a series of checks to validate that your build doesn't violate any of
the rules. To run a check, use the `--check` flag:

```console
$ docker build --check .
```

The available rules for build checks are:

- [`DuplicateStageName`](./duplicate-stage-name.md)

  Stage names should be unique.

- [`FileConsistentCommandCasing`](./file-consistent-command-casing.md)

  All commands within the Dockerfile should use the same casing (either uppercase or lowercase).

- [`FromAsCasing`](./from-as-casing.md)

  The `AS` keyword should match the case of the `FROM` keyword.

- [`JSONArgsRecommended`](./json-args-recommended.md)

  JSON arguments recommended for `ENTRYPOINTCMD` and `CMD` to prevent unintended behavior related to OS signals.

- [`MaintainerDeprecated`](./maintainer-deprecated.md)

  The maintainer instruction is deprecated, use a label to define an image author instead.

- [`MultipleInstructionsDisallowed`](./multiple-instructions-disallowed.md)

  Multiple instructions of the same type should not be used in the same stage.

- [`NoEmptyContinuations`](./no-empty-continuations.md)

  Empty continuation lines will become errors in a future release.

- [`ReservedStageName`](./reserved-stage-name.md)

  Reserved stage names should not be used to name a stage.

- [`SelfConsistentCommandCasing`](./self-consistent-command-casing.md)

  Commands should be in consistent casing (all lowercase or all uppercase).

- [`StageNameCasing`](./stage-name-casing.md)

  Stage names should be lowercase.

- [`UndeclaredArgInFrom`](./undeclared-arg-in-from.md)

  `FROM` command must use declared `ARG` instructions.

- [`UndefinedArg`](./undefined-arg.md)

  `ARG` should be defined before their use.

- [`UndefinedVar`](./undefined-var.md)

  Variables should be defined before their use.

- [`WorkdirRelativePath`](./workdir-relative-path.md)

  Using `WORKDIR` with relative paths without an absolute `WORKDIR` declared within the build can have unexpected results if the base image changes.
