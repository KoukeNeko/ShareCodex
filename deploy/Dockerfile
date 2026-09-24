# Go cross-compiles for the target platform, so multi-arch builds need no
# emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/sharecodex-server ./cmd/sharecodex-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sharecodex-server /usr/local/bin/sharecodex-server
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sharecodex-server"]
