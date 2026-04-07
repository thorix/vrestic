FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vrestic .
RUN CGO_ENABLED=0 go install github.com/restic/restic/cmd/restic@latest

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /vrestic /vrestic
COPY --from=builder /go/bin/restic /usr/bin/restic
ENTRYPOINT ["/vrestic"]
