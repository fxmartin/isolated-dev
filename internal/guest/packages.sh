#!/usr/bin/env bash
# Idempotently converges the guest packages declared in .isolated-dev.toml.
#
# Usage (executed inside the project machine as root):
#   bash -c "$(cat packages.sh)" isolated-dev-packages PACKAGE...
#
# Only missing packages are installed: a rerun after the first `up` touches
# neither the network nor the package database, which is what keeps repeated
# `up` fast and working offline.
set -Eeuo pipefail

# `container machine run` does not guarantee a PATH; see provision.sh.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export DEBIAN_FRONTEND=noninteractive

missing=()
for package in "$@"; do
    status="$(dpkg-query --show --showformat '${db:Status-Status}' "${package}" 2>/dev/null || true)"
    if [ "${status}" != "installed" ]; then
        missing+=("${package}")
    fi
done

if [ "${#missing[@]}" -eq 0 ]; then
    printf 'isolated-dev: all %s declared package(s) already installed\n' "$#"
    exit 0
fi

# The base image ships without package lists (they are cleaned to keep it
# small), so the first install of any package has to fetch them.
apt-get update
apt-get install -y --no-install-recommends "${missing[@]}"
printf 'isolated-dev: installed %s\n' "${missing[*]}"
