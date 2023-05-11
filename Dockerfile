FROM golang:1.19-alpine3.16 as builder

RUN apk --no-cache add build-base

WORKDIR /opt/rsync-sidekick

COPY ./go.mod ./go.sum ./

RUN go mod download -x

COPY . .

RUN go build

RUN go test -v ./...

FROM alpine:3.16

RUN apk --no-cache add bash rsync

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
