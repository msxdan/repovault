# Dockerfile
# Multi-stage build for smaller final image
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache upx

# Install git and ca-certificates (needed for git operations)
RUN apk add --no-cache git ca-certificates openssh-client

# Set working directory
WORKDIR /app

# Copy go mod files
COPY src/go.mod src/go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY src/main.go ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -a -installsuffix cgo -o repovault main.go

RUN upx --best --lzma repovault

# Final stage - minimal image
FROM alpine:3.18

# Install git, openssh (for SSH keys), and ca-certificates
RUN apk add --no-cache \
    git \
    openssh-client \
    ca-certificates \
    tzdata

# Create non-root user for security
RUN addgroup -g 1001 -S gituser && \
    adduser -u 1001 -S gituser -G gituser

# Create necessary directories
RUN mkdir -p /app /backup /config /ssh && \
    chown -R gituser:gituser /app /backup /config /ssh

# Copy binary from builder stage
COPY --from=builder /app/repovault /app/repovault

# Make binary executable
RUN chmod +x /app/repovault

# Configure Git to prevent line ending issues
RUN git config --global core.autocrlf false && \
    git config --global core.filemode false && \
    git config --global user.name "RepoVault" && \
    git config --global user.email "repovault@localhost"

# Switch to non-root user
USER gituser

# Set working directory
WORKDIR /app

# Create SSH directory with proper permissions
RUN mkdir -p ~/.ssh && chmod 700 ~/.ssh

# Expose volume for configuration, SSH keys, and backup data
VOLUME ["/config", "/backup", "/ssh"]

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD pgrep repovault || exit 1

# Default command
ENTRYPOINT ["/app/repovault", "/config/config.yaml", "/backup"]