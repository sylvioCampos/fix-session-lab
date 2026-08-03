# One image, three binaries. The compose file picks which to run.
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /out/exchange  ./cmd/exchange \
 && go build -o /out/oe-client ./cmd/oe-client \
 && go build -o /out/dc-client ./cmd/dc-client

FROM alpine:3.24

WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY config/ /app/config/
COPY spec/ /app/spec/

# Sequence numbers live here and must survive a container restart — the gap
# drill depends on it. compose mounts a named volume per service.
RUN mkdir -p /data/store
VOLUME /data/store

CMD ["exchange"]
