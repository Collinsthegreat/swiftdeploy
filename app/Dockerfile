FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY main.go .
RUN go mod init swiftdeploy-app && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o app .

FROM alpine:3.19 AS runtime
RUN addgroup -g 1001 appgroup && adduser -u 1001 -G appgroup -s /bin/sh -D appuser
WORKDIR /app
COPY --from=builder /build/app .
USER appuser
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:3000/healthz || exit 1
CMD ["./app"]
