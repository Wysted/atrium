# Toolbox image: cluster tooling for the local platform environment.
# The host Docker socket is mounted at runtime so k3d can manage clusters.
FROM debian:trixie-slim

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.37.1
ARG HELM_VERSION=v4.3.0
ARG K3D_VERSION=v5.9.0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash bash-completion ca-certificates curl gettext-base git jq \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/install

RUN curl -fsSLO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256" \
        | awk '{print $1"  kubectl"}' | sha256sum -c - \
    && install -m 0755 kubectl /usr/local/bin/kubectl

RUN curl -fsSLO "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
    && curl -fsSLO "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" \
    && sha256sum -c "helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz.sha256sum" \
    && tar -xzf "helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
    && install -m 0755 "linux-${TARGETARCH}/helm" /usr/local/bin/helm

RUN curl -fsSLO "https://github.com/k3d-io/k3d/releases/download/${K3D_VERSION}/k3d-linux-${TARGETARCH}" \
    && curl -fsSL "https://github.com/k3d-io/k3d/releases/download/${K3D_VERSION}/checksums.txt" \
        | grep " _dist/k3d-linux-${TARGETARCH}$" | sed 's# _dist/# #' | sha256sum -c - \
    && install -m 0755 "k3d-linux-${TARGETARCH}" /usr/local/bin/k3d

WORKDIR /workspace
RUN rm -rf /tmp/install

CMD ["bash"]
