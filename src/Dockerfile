# ---------- Stage 1: build the Go binary ----------
FROM golang:1.27-alpine AS build

WORKDIR /app

# Copy the dependency files first and download modules.
# Docker caches this step, so it only reruns when go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code and compile it into a single static binary.
# CHANGED: cross-compile for x86-64 (amd64), the CPU architecture
# ECS Fargate uses by default, since this Mac builds ARM by default.
COPY product-service.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o product-service .

# ---------- Stage 2: a small image that just runs the binary ----------
# CHANGED: use the amd64 version of Alpine so the whole image is x86-64.
FROM --platform=linux/amd64 alpine:3.22

WORKDIR /app
COPY --from=build /app/product-service .

# Turn off Gin's debug logging inside the container.
ENV GIN_MODE=release

# Document the port the server listens on.
EXPOSE 8080

CMD ["./product-service"]