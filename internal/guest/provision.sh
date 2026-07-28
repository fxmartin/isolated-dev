#!/usr/bin/env bash
# Idempotently configures the isolated-dev guest identity and credentials.
#
# Usage (executed inside the project machine as root):
#   bash -c "$(cat provision.sh)" isolated-dev-provision USER UID GID PUBKEY...
#
# The script never receives, stores, or generates private key material: only
# the authorized public keys passed as trailing arguments are installed.
set -Eeuo pipefail

username="$1"
uid="$2"
gid="$3"
shift 3

home="/home/${username}"

# --- group ------------------------------------------------------------------
if [ -z "$(getent group "${gid}" || true)" ]; then
    groupadd --gid "${gid}" "${username}"
fi

# --- user -------------------------------------------------------------------
uid_owner="$(getent passwd "${uid}" | cut -d: -f1 || true)"
if [ -n "${uid_owner}" ] && [ "${uid_owner}" != "${username}" ]; then
    if [ "${uid_owner}" = "ubuntu" ]; then
        # Ubuntu 24.04 ships an unused `ubuntu` account on UID 1000. It holds no
        # project data, so it is removed to free the host UID.
        userdel --remove ubuntu >/dev/null 2>&1 || userdel ubuntu
    else
        printf 'isolated-dev: UID %s already belongs to guest user %s\n' "${uid}" "${uid_owner}" >&2
        exit 1
    fi
fi

if id -u "${username}" >/dev/null 2>&1; then
    if [ "$(id -u "${username}")" != "${uid}" ] || [ "$(id -g "${username}")" != "${gid}" ]; then
        usermod --uid "${uid}" --gid "${gid}" "${username}"
        chown -R "${uid}:${gid}" "${home}"
    fi
else
    useradd \
        --uid "${uid}" \
        --gid "${gid}" \
        --home-dir "${home}" \
        --create-home \
        --shell /bin/bash \
        "${username}"
fi
install -d -o "${uid}" -g "${gid}" -m 0755 "${home}"
usermod --shell /bin/bash "${username}"
# No password is ever set, so password login cannot succeed even if a future
# sshd drop-in re-enabled it.
passwd --lock "${username}" >/dev/null

# --- administration and Docker ----------------------------------------------
getent group docker >/dev/null || groupadd --system docker
usermod --append --groups docker "${username}"

sudoers_tmp="$(mktemp)"
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "${username}" >"${sudoers_tmp}"
visudo --check --quiet --file="${sudoers_tmp}"
install -o root -g root -m 0440 "${sudoers_tmp}" "/etc/sudoers.d/isolated-dev"
rm -f "${sudoers_tmp}"

# --- authorized public keys --------------------------------------------------
ssh_dir="${home}/.ssh"
install -d -o "${uid}" -g "${gid}" -m 0700 "${ssh_dir}"
keys_tmp="$(mktemp)"
for key in "$@"; do
    printf '%s\n' "${key}" >>"${keys_tmp}"
done
install -o "${uid}" -g "${gid}" -m 0600 "${keys_tmp}" "${ssh_dir}/authorized_keys"
rm -f "${keys_tmp}"

# --- sshd --------------------------------------------------------------------
mkdir -p /etc/ssh/sshd_config.d /run/sshd
sshd_tmp="$(mktemp)"
cat >"${sshd_tmp}" <<EOF
# Managed by isolated-dev. Changes are overwritten on the next \`up\`.
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowAgentForwarding yes
AllowUsers ${username}
EOF
install -o root -g root -m 0644 "${sshd_tmp}" /etc/ssh/sshd_config.d/10-isolated-dev.conf
rm -f "${sshd_tmp}"

ssh-keygen -A >/dev/null
/usr/sbin/sshd -t

sshd_pid=""
if [ -f /run/sshd.pid ]; then
    sshd_pid="$(< /run/sshd.pid)"
fi
if [[ "${sshd_pid}" =~ ^[0-9]+$ ]] && kill -0 "${sshd_pid}" 2>/dev/null; then
    # SIGHUP re-execs sshd with the new drop-in and keeps established sessions
    # alive, so rerunning `up` never drops an open Zed connection.
    kill -HUP "${sshd_pid}"
elif [ -d /run/systemd/system ]; then
    systemctl enable ssh >/dev/null 2>&1 || true
    systemctl start ssh.socket >/dev/null 2>&1 ||
        systemctl start ssh >/dev/null 2>&1 ||
        /usr/sbin/sshd
else
    # Container Machine 1.1.0 can leave systemd unavailable, so sshd is started
    # directly.
    /usr/sbin/sshd
fi

printf 'isolated-dev: guest user %s (%s:%s) configured\n' "${username}" "${uid}" "${gid}"
