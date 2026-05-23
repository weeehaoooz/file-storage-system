# --- Stage 1: Build the statically linked Go binary ---
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Copy module definition files
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application statically
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/server .

# --- Stage 2: Final minimal execution environment ---
FROM alpine:3.20

# Install CA certificates for secure connections
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy the pre-compiled binary from builder stage
COPY --from=builder /app/server /app/server

# Create a data directory for sqlite database and storage volume
RUN mkdir -p /app/data

# Default environment configurations
ENV DB_PATH=/app/data/files.db \
    STORAGE_DIR=/app/data/storage-disk \
    API_PORT=3000 \
    STORAGE_PORT=3001 \
    API_SERVER_URL=http://localhost:3000

# Expose ports for both API and Storage services
EXPOSE 3000 3001

# Run the backend binary
ENTRYPOINT ["/app/server"]
