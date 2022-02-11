# Overview

Frontends are components that run inside BuildKit and convert any build
definition to LLB. There is a special frontend called gateway (`gateway.v0`)
that allows using any image as a frontend.

During development, [Dockerfile frontend](dockerfile.md) (`dockerfile.v0`) is
also part of the BuildKit repo. In the future, this will be moved out, and
Dockerfiles can be built using an external image.
