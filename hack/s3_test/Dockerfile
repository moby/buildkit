ARG MINIO_VERSION=RELEASE.2022-05-03T20-36-08Z
ARG MINIO_MC_VERSION=RELEASE.2022-05-04T06-07-55Z

FROM minio/minio:${MINIO_VERSION} AS minio
FROM minio/mc:${MINIO_MC_VERSION} AS minio-mc
FROM moby/buildkit AS buildkit

FROM debian:bullseye-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends wget ca-certificates containerd curl \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/*

RUN mkdir /test

COPY --from=buildkit /usr/bin/buildkitd /usr/bin/buildctl /bin
COPY --from=minio /opt/bin/minio /bin
COPY --from=minio-mc /usr/bin/mc /bin

COPY . /test
