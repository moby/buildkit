workflow "DCO" {
  on = "push"
  resolves = ["dco-check"]
}

action "dco-check" {
  uses = "./.actions/dco"
}
