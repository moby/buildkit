FROM debian:buster-slim AS base
RUN apt-get update && apt-get install -y --no-install-recommends \
  binutils-arm-linux-gnueabihf \
  binutils-aarch64-linux-gnu \
  binutils-x86-64-linux-gnu \
  binutils-riscv64-linux-gnu
WORKDIR /src


FROM base AS exit-amd64
COPY fixtures/exit.amd64.s .
RUN x86_64-linux-gnu-as -o exit.o exit.amd64.s && x86_64-linux-gnu-ld -o exit -s exit.o

FROM base AS exit-arm64
COPY fixtures/exit.arm64.s .
RUN aarch64-linux-gnu-as -o exit.o exit.arm64.s && aarch64-linux-gnu-ld -o exit -s exit.o

FROM base AS exit-arm
COPY fixtures/exit.arm.s .
RUN arm-linux-gnueabihf-as -o exit.o exit.arm.s && arm-linux-gnueabihf-ld -o exit -s exit.o

FROM base AS exit-riscv64
COPY fixtures/exit.riscv64.s .
RUN riscv64-linux-gnu-as -o exit.o exit.riscv64.s && riscv64-linux-gnu-ld -o exit -s exit.o


FROM golang:1.12-alpine AS generate
WORKDIR /src
COPY --from=exit-amd64 /src/exit amd64
COPY --from=exit-arm64 /src/exit arm64
COPY --from=exit-arm /src/exit arm
COPY --from=exit-riscv64 /src/exit riscv64
COPY generate.go .

RUN go run generate.go amd64 arm64 arm riscv64 && ls -l


FROM scratch
COPY --from=generate /src/*_binary.go  /
