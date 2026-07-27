FROM ubuntu:24.04

ARG BASE_VERSION

LABEL dev.isolated.base-version="${BASE_VERSION}"

ENV container=container
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        dbus \
        docker-compose-v2 \
        docker.io \
        git \
        iproute2 \
        jq \
        openssh-server \
        rsync \
        sudo \
        systemd \
        systemd-sysv && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

RUN > /etc/machine-id && \
    > /var/lib/dbus/machine-id && \
    systemctl set-default multi-user.target && \
    systemctl mask \
        console-getty.service \
        dev-hugepages.mount \
        sys-fs-fuse-connections.mount \
        systemd-tmpfiles-setup.service \
        systemd-update-utmp.service

COPY isolated-dev-dockerd /usr/local/sbin/isolated-dev-dockerd

RUN chmod 0755 /usr/local/sbin/isolated-dev-dockerd
