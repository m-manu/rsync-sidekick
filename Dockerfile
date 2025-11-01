FROM golang:1.24-alpine3.20 AS builder

RUN apk --no-cache add build-base

WORKDIR /opt/rsync-sidekick

COPY ./go.mod ./go.sum ./

RUN go mod download -x

COPY . .

ENV GOROOT="/usr/local/go/"

RUN go build

RUN go test ./...

FROM alpine:3.20

RUN apk --no-cache add bash

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
