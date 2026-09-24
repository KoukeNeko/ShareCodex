FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sharecodex-server ./cmd/sharecodex-server

FROM alpine:3.22
RUN adduser -D -H sharecodex
COPY --from=build /out/sharecodex-server /usr/local/bin/sharecodex-server
USER sharecodex
EXPOSE 8080
ENTRYPOINT ["sharecodex-server"]
