target "base-website" {
  dockerfile = "./hack/dockerfiles/website.Dockerfile"
  target = "base"
  output = ["type=docker"]
  tags = ["buildkit-website:local"]
}

target "update-website" {
  dockerfile = "./hack/dockerfiles/website.Dockerfile"
  target = "release"
  output = ["./release-site"]
}
