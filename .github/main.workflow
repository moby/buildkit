workflow "DCO" {
  on = "pull_request"
  resolves = ["dco-check"]
}

action "dco-check" {
  uses = "./.actions/dco"
}
