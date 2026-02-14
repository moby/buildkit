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

# make GNUPGHOME global (not per-user), point into sign fixtures
export GNUPGHOME="$BUILDKIT_TEST_SIGN_FIXTURES/gnupg"

mkdir -p "$GNUPGHOME"

cd "$BUILDKIT_TEST_SIGN_FIXTURES"

cat >"${USERNAME}.gpg.conf" <<'EOF'
%no-protection
Key-Type: RSA
Key-Length: 2048
Key-Usage: cert
Name-Real: __NAME_REAL__
Name-Email: __NAME_EMAIL__
Expire-Date: 0
%commit
EOF
# replace placeholders in the generated conf
sed -i'' "s|__NAME_REAL__|${USERNAME} Test User|g" "${USERNAME}.gpg.conf"
sed -i'' "s|__NAME_EMAIL__|${USERNAME}@example.com|g" "${USERNAME}.gpg.conf"

gpg --batch --no-tty --generate-key --pinentry-mode loopback "${USERNAME}.gpg.conf"

FP="$(gpg --list-keys --with-colons "${USERNAME}@example.com" | awk -F: '/^fpr:/ {print $10; exit}')"

gpg --no-tty --pinentry-mode loopback --passphrase test --quick-add-key "$FP" rsa2048 sign 1y

gpg --list-secret-keys --with-subkey-fingerprint

# create a single global git-gpg wrapper that uses the shared GNUPGHOME
cat >"${BUILDKIT_TEST_SIGN_FIXTURES}/git_gpg_sign.sh" <<'EOF'
#!/bin/sh
if [ -z "$BUILDKIT_TEST_SIGN_FIXTURES" ]; then
  echo "BUILDKIT_TEST_SIGN_FIXTURES is not set" >&2
  exit 1
fi
export GNUPGHOME="$BUILDKIT_TEST_SIGN_FIXTURES/gnupg"
exec gpg --batch --pinentry-mode loopback --passphrase test "$@"
EOF
chmod +x "${BUILDKIT_TEST_SIGN_FIXTURES}/git_gpg_sign.sh"

# create a user-specific gitconfig that points to the single wrapper and key
cat >"${BUILDKIT_TEST_SIGN_FIXTURES}/${USERNAME}.gpg.gitconfig" <<EOF
[gpg]
    program = ${BUILDKIT_TEST_SIGN_FIXTURES}/git_gpg_sign.sh
[user]
    signingkey = $FP
    email = ${USERNAME}@example.com
    name = Test Git ${USERNAME}
[commit]
    gpgsign = true
EOF

gpg --armor --export "$FP" >"${USERNAME}.gpg.pub"

# generate pre-signed detached signature fixture for HTTP checksum assist tests
printf '%s' "http payload for detached signature verification" >"${USERNAME}.http.artifact"
gpg --batch --yes --pinentry-mode loopback --passphrase test \
  --local-user "$FP" --armor \
  --output "${USERNAME}.http.artifact.asc" \
  --detach-sign "${USERNAME}.http.artifact"
