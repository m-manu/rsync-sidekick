FROM golang:1.17-alpine3.14 as builder

RUN apk --no-cache add build-base

WORKDIR /opt/rsync-sidekick

COPY ./go.mod ./go.sum ./

RUN go mod download -x

COPY . .

RUN go build

RUN go test ./...

FROM alpine:3.14

RUN apk --no-cache add bash rsync

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
