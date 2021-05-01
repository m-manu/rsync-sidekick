FROM golang:1.16.3-alpine3.13 as builder

WORKDIR /opt/rsync-sidekick

ADD . ./

RUN go build

FROM alpine:3.13

RUN apk --no-cache add bash rsync

COPY --from=builder /opt/rsync-sidekick/rsync-sidekick /bin
