target "buildkit" {
  context = "../../"
  cache-from = ["type=gha,scope=binaries"]
}

target "default" {
  contexts = {
    buildkit = "target:buildkit"
  }
  tags = ["moby/buildkit:s3test"]
}
