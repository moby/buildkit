FROM public.ecr.aws/debian/debian:bullseye-slim AS build

RUN cat /dev/urandom | head -c 100 | sha256sum > unique_first
RUN cat /dev/urandom | head -c 100 | sha256sum > unique_second

FROM scratch
COPY --link --from=build /unique_first /
COPY --link --from=build /unique_second /