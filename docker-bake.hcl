variable "ALPINE_VERSION" {
  default = null
}

variable "GO_VERSION" {
  default = null
}

variable "NODE_VERSION" {
  default = null
}

target "_common" {
  args = {
    ALPINE_VERSION = ALPINE_VERSION
    GO_VERSION = GO_VERSION
    NODE_VERSION = NODE_VERSION
    BUILDKIT_CONTEXT_KEEP_GIT_DIR = 1
  }
}

group "validate" {
  targets = ["lint", "validate-vendor", "validate-doctoc", "validate-generated-files", "validate-shfmt"]
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
