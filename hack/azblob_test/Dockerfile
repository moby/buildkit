FROM moby/buildkit AS buildkit

FROM debian:bullseye-slim
RUN apt-get update \
  && curl -fsSL https://deb.nodesource.com/setup_18.x | bash - \
  && apt-get install -y --no-install-recommends ca-certificates containerd curl nodejs npm procps \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/* \
  && npm install -g azurite@3.18.0 \
  && mkdir /test \
  && mkdir /tmp/azurite \
  && curl -sL https://aka.ms/InstallAzureCLIDeb | bash

COPY --link --from=buildkit /usr/bin/buildkitd /usr/bin/buildctl /bin/

COPY --link . /test
