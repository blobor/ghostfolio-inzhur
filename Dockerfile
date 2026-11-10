# syntax=docker/dockerfile:1

# The build stage runs on the platform of the builder and cross-compiles
# for the target platform. Thus a multi-platform build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build

WORKDIR /src

# go.sum does not exist while the module has no dependencies.
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/inzhur2gf ./cmd/inzhur2gf

# The data directory must belong to the user of the final image.
RUN mkdir /out/data

# The final image has no shell and no package manager. It has the CA
# certificates, which the HTTPS calls need.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

COPY --from=build /out/inzhur2gf /usr/local/bin/inzhur2gf
COPY --from=build --chown=nonroot:nonroot --chmod=700 /out/data /data

ENV DATA_DIR=/data
VOLUME /data

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/inzhur2gf"]
