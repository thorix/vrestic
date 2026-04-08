FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vrestic .
RUN CGO_ENABLED=0 go install github.com/restic/restic/cmd/restic@latest

FROM alpine:3.21
RUN apk --no-cache add ca-certificates openssh-client
COPY --from=builder /vrestic /vrestic
COPY --from=builder /go/bin/restic /usr/bin/restic
USER 65534:65534
ENTRYPOINT ["/vrestic"]
