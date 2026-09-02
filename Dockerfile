# builder stage
FROM registry.suse.com/bci/bci-base:16.1 AS builder
ARG TARGETPLATFORM

# fetched from goreleaser build process
COPY $TARGETPLATFORM/hauler /hauler

RUN echo "hauler:x:1001:1001::/home/hauler:" > /etc/passwd \
&& echo "hauler:x:1001:hauler" > /etc/group \
&& mkdir /home/hauler \
&& mkdir /store \
&& mkdir /fileserver \
&& mkdir /registry

# release stage
FROM alpine:3.23 AS release

RUN apk update && \
    apk upgrade --no-cache && \
    apk add --no-cache openssl ca-certificates yq

RUN addgroup -g 1001 -S hauler && \
    adduser -u 1001 -S -G hauler -h /home/hauler hauler && \
    mkdir -p /home/hauler /tmp /store /registry /fileserver && \
    chown -R hauler:hauler /home/hauler /tmp /store /registry /fileserver && \
    chown -R hauler:hauler /usr/local/share/ca-certificates /etc/ssl/certs && \
    chown hauler:hauler /etc/ca-certificates.conf && \
    chmod 755 /usr/local/share/ca-certificates /etc/ssl/certs /usr/sbin/update-ca-certificates

COPY --from=builder --chown=hauler:hauler /home/hauler/. /home/hauler
COPY --from=builder --chown=hauler:hauler /hauler /usr/local/bin/hauler

USER hauler
RUN update-ca-certificates
ENTRYPOINT [ "hauler" ]

# debug stage
FROM alpine:3.23 AS debug
ARG TARGETARCH=amd64

RUN apk update && \
    apk upgrade --no-cache && \
    apk add --no-cache git ca-certificates yq

RUN addgroup -g 1001 hauler && \
    adduser -u 1001 -G hauler -s /bin/sh -D hauler

# Allow update-ca-certificates to run after switching away from root.
RUN chown -R hauler:hauler /usr/local/share/ca-certificates && \
    chown -R hauler:hauler /etc/ssl/certs && \
    chown hauler:hauler /etc/ca-certificates.conf && \
    chmod 755 /usr/local/share/ca-certificates && \
    chmod 755 /etc/ssl/certs && \
    chmod 755 /usr/sbin/update-ca-certificates

ENV KUBECTL_VERSION="1.33.3" \
    HELM_VERSION="3.18.6"

RUN wget -O kubectl https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/

RUN wget -O helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz && \
    tar -zxvf helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz && \
    mv linux-${TARGETARCH}/helm /usr/local/bin/helm && \
    rm -rf linux-${TARGETARCH} helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz

ENV HELM_CONFIG_HOME=/home/hauler/.config/helm \
    HELM_DATA_HOME=/home/hauler/.local/share/helm \
    HELM_CACHE_HOME=/home/hauler/.cache/helm

RUN mkdir -p "$HELM_CONFIG_HOME" "$HELM_DATA_HOME" "$HELM_CACHE_HOME" && \
    chown -R hauler:hauler /home/hauler

USER hauler

RUN update-ca-certificates && \
    helm plugin install https://github.com/databus23/helm-diff && \
    kubectl version --client && \
    helm version --short && \
    helm plugin list

COPY --from=builder --chown=hauler:hauler /home/hauler/. /home/hauler
COPY --from=builder --chown=hauler:hauler /hauler /usr/local/bin/hauler

WORKDIR /home/hauler
