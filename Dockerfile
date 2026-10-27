# syntax=docker/dockerfile:1

# Build the Go server as a static linux/amd64 binary (the EC2 host is x86_64).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/cheersquad ./cmd/cheersquad

# The Angular UI will get its own build stage here and copy into /app/web.

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/cheersquad /app/cheersquad
ENV ADDR=:8080 STATIC_DIR=/app/web
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/app/cheersquad", "-healthcheck"]
ENTRYPOINT ["/app/cheersquad"]
