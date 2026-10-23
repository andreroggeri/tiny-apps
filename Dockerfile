# Cross-compiles on the build machine's platform; no emulation needed for arm64.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /pombo ./cmd/pombo

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pombo /pombo
ENV POMBO_DATA_DIR=/data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/pombo"]
