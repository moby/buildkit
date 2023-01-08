FROM moby/buildkit AS buildkit

FROM debian:bullseye-slim
ARG TARGETARCH
ENV KUBO_VERSION=0.17.0
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates containerd curl \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/* \
  && curl -o kubo_v${KUBO_VERSION}.tar.gz -Ls https://github.com/ipfs/kubo/releases/download/v${KUBO_VERSION}/kubo_v${KUBO_VERSION}_${TARGETOS:-linux}-${TARGETARCH:-amd64}.tar.gz \
  && tar -xvzf kubo_v${KUBO_VERSION}.tar.gz \
  && mv ./kubo/ipfs /usr/local/bin/ipfs \
  && mkdir /test

COPY --link --from=buildkit /usr/bin/buildkitd /usr/bin/buildctl /bin/

COPY --link . /test
