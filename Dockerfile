# Dockerfile for the tunnel server
# Uses multi-stage build to keep final image small

# Stage 1: Build the Go binary
# We use the full Go image to compile
FROM golang:1.21-alpine AS builder

# Set working directory inside container
WORKDIR /app

# Copy go.mod and go.sum first (Docker caches layers)
# If these don't change, Docker skips re-downloading deps
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the server binary
# CGO_ENABLED=0 = pure Go, no C dependencies (smaller, portable)
# -o server = output filename
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

# Stage 2: Minimal runtime image
# Alpine is tiny (~5MB) - we only need the binary
FROM alpine:latest

WORKDIR /app

# Copy just the binary from builder stage
COPY --from=builder /app/server .

# Expose the port our server listens on
EXPOSE 8080

# Run the server
CMD ["./server"]
