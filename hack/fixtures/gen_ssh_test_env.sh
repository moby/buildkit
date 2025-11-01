#!/usr/bin/env sh

set -e

# require BUILDKIT_TEST_SIGN_FIXTURES to be set
if [ -z "$BUILDKIT_TEST_SIGN_FIXTURES" ]; then
  echo "BUILDKIT_TEST_SIGN_FIXTURES is not set"
  exit 1
fi

# require username parameter
if [ -z "$1" ]; then
  echo "Usage: $0 <username>"
  echo "Example: $0 user1"
  exit 1
fi

USERNAME="$1"

set -x

FIXTURES="$BUILDKIT_TEST_SIGN_FIXTURES"
mkdir -p "$FIXTURES"
cd "$FIXTURES"

mkdir -p .ssh
chmod 700 .ssh
KEY_BASE=".ssh/${USERNAME}.id_ed25519"
if [ ! -f "$KEY_BASE" ]; then
  ssh-keygen -t ed25519 -C "${USERNAME}@example.com" -f "$KEY_BASE" -N "" -q
fi

# read user's public key
PUBKEY_LINE="$(cat "${KEY_BASE}.pub")"
if [ -z "$PUBKEY_LINE" ]; then
  echo "failed to read public key" >&2
  exit 1
fi

# create per-user ssh signing wrapper in fixtures root; hardcode username in key path
cat >"${FIXTURES}/${USERNAME}.git_ssh_sign.sh" <<'EOF'
#!/bin/sh
set -eu
unset SSH_AUTH_SOCK || true
base="$(cd "$(dirname "$0")" && pwd)"

# Parse args from Git
buf=""
out=""
args=""
while [ $# -gt 0 ]; do
    case "$1" in
        -U)
            shift
            buf="$1"
            out="${buf}.sig"
            ;;
        /tmp/.git_signing_key_tmp*)
            args="$args -f $base/.ssh/__USER__.id_ed25519"
            ;;
        *)
            args="$args $1"
            ;;
    esac
    shift
done

# Fallback sanity check
[ -n "$buf" ] || { echo "missing -U buffer" >&2; exit 1; }

# Sign manually without -U
ssh-keygen -Y sign -f "$base/.ssh/__USER__.id_ed25519" -n git "$buf" >/dev/null

# ssh-keygen wrote "$buf.sig"; rename to what Git expects
mv "$buf.sig" "$out"
EOF

# inject username into wrapper
sed -i "s/__USER__/${USERNAME}/g" "${FIXTURES}/${USERNAME}.git_ssh_sign.sh"
chmod +x "${FIXTURES}/${USERNAME}.git_ssh_sign.sh"

# per-user gitconfig pointing to per-user wrapper; no username prefix in signingkey
cat >"${FIXTURES}/${USERNAME}.ssh.gitconfig" <<EOF
[gpg]
	format = ssh
[user]
	signingkey = ${PUBKEY_LINE}
	name = Test Git ${USERNAME}
	email = ${USERNAME}@example.com
[commit]
	gpgsign = true
[gpg "ssh"]
	program = ${FIXTURES}/${USERNAME}.git_ssh_sign.sh
EOF

# export user-labeled public key
cp "${KEY_BASE}.pub" "${USERNAME}.ssh.pub"
