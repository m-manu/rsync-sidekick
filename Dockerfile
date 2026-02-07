FROM golang:1.24-alpine3.20@sha256:9f98e9893fbc798c710f3432baa1e0ac6127799127c3101d2c263c3a954f0abe AS builder

RUN apk --no-cache add build-base

WORKDIR /opt/rsync-sidekick

COPY ./go.mod ./go.sum ./

RUN go mod download -x

COPY . .

ENV GOROOT="/usr/local/go/"

RUN go build

RUN go test ./...

FROM alpine:3.20@sha256:a4f4213abb84c497377b8544c81b3564f313746700372ec4fe84653e4fb03805

RUN apk --no-cache add bash

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
