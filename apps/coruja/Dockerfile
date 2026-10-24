# Cross-compiles on the build machine's platform; no emulation needed for arm64.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
# Standard library only: nothing to download.
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /coruja ./cmd/coruja

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /coruja /coruja
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/coruja"]
