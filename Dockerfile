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
    apk add --no-cache curl openssl ca-certificates yq

RUN addgroup -g 1001 -S hauler && \
    adduser -u 1001 -S -G hauler -h /home/hauler hauler && \
    mkdir -p /home/hauler /tmp /store /registry /fileserver && \
    chown -R hauler:hauler /home/hauler /tmp /store /registry /fileserver

COPY --from=builder --chown=hauler:hauler /home/hauler/. /home/hauler
COPY --from=builder --chown=hauler:hauler /hauler /hauler

USER hauler
ENTRYPOINT [ "/hauler" ]

# debug stage
FROM registry.suse.com/bci/bci-base:16.1 AS debug

COPY --from=builder /var/lib/ca-certificates/ca-bundle.pem /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder --chown=hauler:hauler /home/hauler/. /home/hauler
COPY --from=builder --chown=hauler:hauler /hauler /usr/local/bin/hauler

USER hauler
WORKDIR /home/hauler
