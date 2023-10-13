variable "ALPINE_VERSION" {
  default = null
}

variable "GO_VERSION" {
  default = null
}

variable "NODE_VERSION" {
  default = null
}

variable "BUILDKITD_TAGS" {
  default = null
}

variable "HTTP_PROXY" {
  default = null
}

variable "HTTPS_PROXY" {
  default = null
}

variable "NO_PROXY" {
  default = null
}

variable "GOBUILDFLAGS" {
  default = null
}

variable "VERIFYFLAGS" {
  default = null
}

variable "CGO_ENABLED" {
  default = null
}

# Defines the output folder
variable "DESTDIR" {
  default = ""
}
function "bindir" {
  params = [defaultdir]
  result = DESTDIR != "" ? DESTDIR : "./bin/${defaultdir}"
}

target "_common" {
  args = {
    ALPINE_VERSION = ALPINE_VERSION
    GO_VERSION = GO_VERSION
    NODE_VERSION = NODE_VERSION
    BUILDKITD_TAGS = BUILDKITD_TAGS
    HTTP_PROXY = HTTP_PROXY
    HTTPS_PROXY = HTTPS_PROXY
    NO_PROXY = NO_PROXY
    GOBUILDFLAGS = GOBUILDFLAGS
    VERIFYFLAGS = VERIFYFLAGS
    CGO_ENABLED = CGO_ENABLED
    BUILDKIT_CONTEXT_KEEP_GIT_DIR = 1
  }
}

group "default" {
  targets = ["binaries"]
}

target "binaries" {
  inherits = ["_common"]
  target = "binaries"
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

target "binaries-smoketest" {
  inherits = ["_common"]
  target = "binaries-smoketest"
  output = ["type=cacheonly"]
  platforms = [
    "linux/amd64",
    "linux/arm/v7",
    "linux/arm64",
    "linux/s390x",
    "linux/ppc64le",
    "linux/riscv64"
  ]
}

target "release" {
  inherits = ["binaries-cross"]
  target = "release"
  output = [bindir("release")]
}

target "integration-tests-base" {
  inherits = ["_common"]
  target = "integration-tests-base"
  output = ["type=cacheonly"]
}

target "integration-tests" {
  inherits = ["integration-tests-base"]
  target = "integration-tests"
}

group "validate" {
  targets = ["lint", "validate-vendor", "validate-doctoc", "validate-generated-files", "validate-shfmt", "validate-docs"]
}

target "lint" {
  name = "lint-${buildtags.name}"
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/lint.Dockerfile"
  output = ["type=cacheonly"]
  target = buildtags.target
  args = {
    BUILDTAGS = buildtags.tags
  }
  matrix = {
    buildtags = [
      { name = "default", tags = "", target = "golangci-lint" },
      { name = "labs", tags = "dfrunsecurity", target = "golangci-lint" },
      { name = "nydus", tags = "nydus", target = "golangci-lint" },
      { name = "yaml", tags = "", target = "yamllint" },
      { name = "proto", tags = "", target = "protolint" },
    ]
  }
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

target "validate-doctoc" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/doctoc.Dockerfile"
  target = "validate-toc"
  output = ["type=cacheonly"]
}

target "validate-authors" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/authors.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "validate-docs" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/docs.Dockerfile"
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

target "doctoc" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/doctoc.Dockerfile"
  target = "update"
  output = ["."]
}

target "authors" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/authors.Dockerfile"
  target = "update"
  output = ["."]
}

target "docs" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/docs.Dockerfile"
  target = "update"
  output = ["./docs"]
}

target "mod-outdated" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/vendor.Dockerfile"
  target = "outdated"
  no-cache-filter = ["outdated"]
  output = ["type=cacheonly"]
}
