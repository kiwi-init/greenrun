ARG UBUNTU_VERSION=24.04
FROM ubuntu:${UBUNTU_VERSION}

ARG TARGETARCH
ARG NODE_VERSION=24.16.0

ENV DEBIAN_FRONTEND=noninteractive
ENV RUNNER_TOOL_CACHE=/opt/hostedtoolcache
ENV AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl docker-buildx docker.io gh git git-lfs jq openssh-client \
      python3 python3-pip python3-venv rsync sudo unzip xz-utils zip \
    && rm -rf /var/lib/apt/lists/*

RUN case "$TARGETARCH" in \
      amd64) node_arch=x64 ;; \
      arm64) node_arch=arm64 ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac \
    && base="node-v${NODE_VERSION}-linux-${node_arch}" \
    && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/${base}.tar.xz" \
    && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt" \
    && grep " ${base}.tar.xz\$" SHASUMS256.txt | sha256sum -c - \
    && tar -xJf "${base}.tar.xz" --strip-components=1 -C /usr/local \
    && rm -f "${base}.tar.xz" SHASUMS256.txt \
    && corepack enable

RUN useradd -m -s /bin/bash runner \
    && usermod -aG docker,sudo runner \
    && printf 'runner ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/runner \
    && mkdir -p /github/workspace /github/home /opt/hostedtoolcache \
    && chown -R runner:runner /github /opt/hostedtoolcache

WORKDIR /github/workspace
CMD ["tail", "-f", "/dev/null"]
