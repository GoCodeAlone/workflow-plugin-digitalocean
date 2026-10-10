#!/usr/bin/env bash
set -euo pipefail

# The reviewed hosted runner uses its signed Ubuntu package repository.
if ! test -x /usr/bin/rg; then
  sudo apt-get update -qq
  sudo apt-get install -y ripgrep
fi
test "$(command -v rg)" = /usr/bin/rg
test "$(env -i PATH=/opt/hostedtoolcache/go/1.27.2/x64/bin:/usr/bin:/bin /bin/bash -c 'command -v rg')" = /usr/bin/rg
/usr/bin/rg --version
printf 'secrets.TEST\n' | /usr/bin/rg -q -P 'secrets\.(?!GITHUB_TOKEN\b)'
if printf 'secrets.GITHUB_TOKEN\n' | /usr/bin/rg -q -P 'secrets\.(?!GITHUB_TOKEN\b)'; then
  rg_nonmatch_status=0
else
  rg_nonmatch_status=$?
fi
test "${rg_nonmatch_status}" -eq 1
