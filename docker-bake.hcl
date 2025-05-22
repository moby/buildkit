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

variable "GOLANGCI_LINT_MULTIPLATFORM" {
  default = null
}

variable "ARCHUTIL_MULTIPLATFORM" {
  default = null
}

# Defines the output folder
variable "DESTDIR" {
  default = ""
}

variable "TEST_COVERAGE" {
  default = null
}

variable "TEST_IMAGE_NAME" {
  default = "buildkit-tests"
}

variable "TEST_CONTEXT" {
  default = "."
  description = "Context for building the test image"
}

variable "TEST_BINARIES_CONTEXT" {
  default = TEST_CONTEXT
  description = "Context for building the buildkitd for test image"
}

variable "BUILDKIT_SYNTAX" {
  default = null
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

target "binaries-for-test" {
  inherits = ["_common"]
  target = "binaries-for-test"
  output = [bindir("build")]
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

target "integration-tests-binaries" {
  inherits = ["_common"]
  target = "binaries"
  context = TEST_BINARIES_CONTEXT
}

target "integration-tests" {
  inherits = ["integration-tests-base"]
  target = "integration-tests"
  context = TEST_CONTEXT
  contexts = TEST_CONTEXT != TEST_BINARIES_CONTEXT ? {
    "binaries" = "target:integration-tests-binaries"
  } : null
  args = {
    GOBUILDFLAGS = TEST_COVERAGE == "1" ? "-cover" : null
    BUILDKIT_SYNTAX = BUILDKIT_SYNTAX
  }
  output = [
    "type=docker,name=${TEST_IMAGE_NAME}",
  ]
}

group "validate" {
  targets = ["lint", "validate-vendor", "validate-doctoc", "validate-dockerfile", "validate-generated-files", "validate-archutil", "validate-shfmt", "validate-docs", "validate-docs-dockerfile"]
}

target "lint" {
  name = "lint-${buildtags.name}"
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/lint.Dockerfile"
  output = ["type=cacheonly"]
  target = buildtags.target
  args = {
    TARGETNAME = buildtags.name
    BUILDTAGS = buildtags.tags
    GOLANGCI_FROM_SOURCE = "true"
  }
  platforms = ( buildtags.target == "golangci-lint" || buildtags.name == "gopls" ) && GOLANGCI_LINT_MULTIPLATFORM != null ? [
    "freebsd/amd64",
    "linux/amd64",
    "linux/arm64",
    "linux/s390x",
    "linux/ppc64le",
    "linux/riscv64",
    "windows/amd64",
    "windows/arm64"
  ] : []
  matrix = {
    buildtags = [
      { name = "default", tags = "", target = "golangci-lint" },
      { name = "labs", tags = "dfrunsecurity dfparents dfexcludepatterns", target = "golangci-lint" },
      { name = "nydus", tags = "nydus", target = "golangci-lint" },
      { name = "yaml", tags = "", target = "yamllint" },
      { name = "golangci-verify", tags = "", target = "golangci-verify" },
      { name = "proto", tags = "", target = "protolint" },
      { name = "gopls", tags = "", target = "gopls-analyze" }
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

target "validate-archutil" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/archutil.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
  platforms = ARCHUTIL_MULTIPLATFORM != null ? [
    "linux/amd64",
    "linux/arm64"
  ] : []
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

target "validate-docs-dockerfile" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/docs-dockerfile.Dockerfile"
  target = "validate"
  output = ["type=cacheonly"]
}

target "validate-dockerfile" {
  matrix = {
    dockerfile = [
      "Dockerfile",
      "./hack/dockerfiles/archutil.Dockerfile",
      "./hack/dockerfiles/authors.Dockerfile",
      "./hack/dockerfiles/docs-dockerfile.Dockerfile",
      "./hack/dockerfiles/docs.Dockerfile",
      "./hack/dockerfiles/doctoc.Dockerfile",
      "./hack/dockerfiles/generated-files.Dockerfile",
      "./hack/dockerfiles/govulncheck.Dockerfile",
      "./hack/dockerfiles/lint.Dockerfile",
      "./hack/dockerfiles/shfmt.Dockerfile",
      "./hack/dockerfiles/vendor.Dockerfile",
      "./frontend/dockerfile/cmd/dockerfile-frontend/Dockerfile",
    ]
  }
  name = "validate-dockerfile-${md5(dockerfile)}"
  inherits = ["_common"]
  dockerfile = dockerfile
  call = "check"
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

target "archutil" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/archutil.Dockerfile"
  target = "update"
  output = ["./util/archutil"]
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

target "docs-dockerfile" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/docs-dockerfile.Dockerfile"
  target = "update"
  output = ["./frontend/dockerfile/docs/rules"]
}

target "mod-outdated" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/vendor.Dockerfile"
  target = "outdated"
  no-cache-filter = ["outdated"]
  output = ["type=cacheonly"]
}

variable "GOVULNCHECK_FORMAT" {
  default = null
}

target "govulncheck" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/govulncheck.Dockerfile"
  target = "output"
  args = {
    FORMAT = GOVULNCHECK_FORMAT
  }
  no-cache-filter = ["run"]
  output = ["${DESTDIR}"]
}
