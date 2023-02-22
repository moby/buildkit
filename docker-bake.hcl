variable "ALPINE_VERSION" {
  default = null
}

variable "GO_VERSION" {
  default = null
}

target "_common" {
  args = {
    ALPINE_VERSION = ALPINE_VERSION
    GO_VERSION = GO_VERSION
    BUILDKIT_CONTEXT_KEEP_GIT_DIR = 1
  }
}

group "validate" {
  targets = ["validate-vendor"]
}

target "validate-vendor" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/vendor.Dockerfile"
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

target "authors" {
  inherits = ["_common"]
  dockerfile = "./hack/dockerfiles/authors.Dockerfile"
  target = "update"
  output = ["."]
}
