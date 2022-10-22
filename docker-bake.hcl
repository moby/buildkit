variable "GO_VERSION" {
  default = "1.19"
}

variable "BUILDKITD_TAGS" {
  default = ""
}

variable "IMAGE_TARGET" {
  default = ""
}

variable "FRONTEND_CHANNEL" {
  default = "mainline"
}
variable "FRONTEND_BUILDTAGS" {
  default = ""
}

# Defines the output folder
variable "DESTDIR" {
  default = ""
}
function "bindir" {
  params = [defaultdir]
  result = DESTDIR != "" ? DESTDIR : "./bin/${defaultdir}"
}

# Special target: https://github.com/docker/metadata-action#bake-definition
target "meta-helper" {
  tags = [IMAGE_TARGET != "" ? "moby/buildkit:local-${IMAGE_TARGET}" : "moby/buildkit:local"]
}
target "frontend-meta-helper" {
  tags = [FRONTEND_CHANNEL != "mainline" ? "docker/dockerfile:local-${FRONTEND_CHANNEL}" : "docker/dockerfile:local"]
}

target "_common" {
  args = {
    GO_VERSION = GO_VERSION
    BUILDKIT_CONTEXT_KEEP_GIT_DIR = 1
  }
}

group "default" {
  targets = ["binaries"]
}

group "validate" {
  targets = ["lint", "validate-vendor", "validate-generated-files", "validate-shfmt"]
}

target "lint" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/lint.Dockerfile"
  output = ["type=cacheonly"]
}

target "validate-vendor" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/vendor.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "validate-generated-files" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/generated-files.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "validate-shfmt" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/shfmt.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "validate-authors" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/authors.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "vendor" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/vendor.Dockerfile"
  target = "update"
  output = ["."]
}

target "generated-files" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/generated-files.Dockerfile"
  target = "update"
  output = ["."]
}

target "shfmt" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/shfmt.Dockerfile"
  target = "update"
  output = ["."]
}

target "authors" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/authors.Dockerfile"
  target = "update"
  output = ["."]
}

target "binaries" {
  inherits = ["_common"]
  target = "binaries"
  args = {
    BUILDKITD_TAGS = BUILDKITD_TAGS
  }
  output = [bindir("build")]
}

target "binaries-cross" {
  inherits = ["binaries"]
  output = [bindir("cross")]
  platforms = [
    "darwin/amd64",
    "darwin/arm64",
    "linux/amd64",
    "linux/arm/v7",
    "linux/arm64",
    "linux/s390x",
    "linux/ppc64le",
    "linux/riscv64",
    "windows/amd64",
    "windows/arm64"
  ]
}

target "release" {
  inherits = ["binaries-cross"]
  target = "release"
  output = [bindir("release")]
}

target "image" {
  inherits = ["_common", "meta-helper"]
  target = IMAGE_TARGET
  output = ["type=docker,buildinfo-attrs=true"]
}

target "image-all" {
  inherits = ["image"]
  platforms = [
    "linux/amd64",
    "linux/arm/v7",
    "linux/arm64",
    "linux/s390x",
    "linux/ppc64le",
    "linux/riscv64"
  ]
}

target "frontend-image" {
  inherits = ["_common", "frontend-meta-helper"]
  dockerfile = "./frontend/dockerfile/cmd/dockerfile-frontend/Dockerfile"
  args = {
    CHANNEL = FRONTEND_CHANNEL
    BUILDTAGS = FRONTEND_BUILDTAGS
  }
  output = ["type=docker,buildinfo-attrs=true"]
}

target "frontend-image-all" {
  inherits = ["frontend-image"]
  platforms = [
    "linux/amd64",
    "linux/arm/v7",
    "linux/arm64",
    "linux/mips",
    "linux/mipsle",
    "linux/mips64",
    "linux/mips64le",
    "linux/s390x",
    "linux/ppc64le",
    "linux/riscv64"
  ]
}

target "integration-tests-base" {
  inherits = ["_common"]
  target = "integration-tests-base"
  output = ["type=cacheonly"]
}
