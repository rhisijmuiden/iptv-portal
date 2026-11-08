#!/usr/bin/env bash
# Prints the next test (pre-release) version, based on the tags in the
# repository. Used by the Test release workflow.
#
#   latest stable v0.5.0, no newer pre-release   -> v0.5.1-rc.1
#   latest stable v0.5.0, newest v0.5.1-rc.2     -> v0.5.1-rc.3
#   latest stable v0.5.0, newest v0.6.0-beta.1   -> v0.6.0-rc.1
#
# To start a new minor or major version, push its first pre-release tag
# (for example v0.6.0-rc.1) or release it as stable; later test releases
# continue from there.
set -euo pipefail

tags=$(git tag -l 'v*')

stable=$(grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' <<<"$tags" | sort -V | tail -n1 || true)
if [ -n "$stable" ]; then
  IFS=. read -r major minor patch <<<"${stable#v}"
  base="v${major}.${minor}.$((patch + 1))"
else
  base="v0.0.1"
fi

# A pre-release of a higher version than base (made by hand) wins.
pre_bases=$(grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-(alpha|beta|rc)\.[0-9]+$' <<<"$tags" | sed 's/-.*//' || true)
top=$(printf '%s\n%s\n' "$base" "$pre_bases" | grep . | sort -uV | tail -n1)
if [ "$top" != "$base" ]; then
  base="$top"
fi

last=$(grep -E "^${base//./\\.}-rc\.[0-9]+$" <<<"$tags" | sed 's/.*-rc\.//' | sort -n | tail -n1 || true)
echo "${base}-rc.$(( ${last:-0} + 1 ))"
