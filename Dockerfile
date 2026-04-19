ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.23

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder

RUN apk --no-cache add build-base

WORKDIR /opt/rsync-sidekick

COPY ./go.mod ./go.sum ./

RUN go mod download -x

COPY . .

ENV GOROOT="/usr/local/go/"

RUN go build

RUN go test ./...

FROM alpine:${ALPINE_VERSION}

RUN apk --no-cache add bash

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
