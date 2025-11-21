# --- Stage 1: Build the Go Binary & Playwright Installer ---
FROM golang:1.23 AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 1. Build your application
RUN CGO_ENABLED=0 GOOS=linux go build -o downloader main.go

# 2. Build the Playwright-Go CLI tool
# This allows us to install the specific driver version (v1.52.0) in the next stage
# without needing the full Go toolchain.
RUN GOOS=linux go build -o playwright-cli github.com/playwright-community/playwright-go/cmd/playwright

# --- Stage 2: The Runtime Environment ---
# We switch to standard Ubuntu because the official Playwright image 
# is optimized for Node/Python and causes version conflicts with Go.
FROM ubuntu:jammy

# Install system dependencies needed for the browser and Xvfb
RUN apt-get update && apt-get install -y \
    xvfb \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/downloader .
COPY --from=builder /app/playwright-cli .

# 3. Install the Drivers and Dependencies
# This uses the Go-specific CLI to install the exact version (v1.52.0) 
# requested by your go.mod, plus OS dependencies.
RUN ./playwright-cli install --with-deps

# Clean up the installer to save space
RUN rm playwright-cli

# Verify binary exists
RUN ls -la /app/downloader

# Command to run
CMD ["sh", "-c", "xvfb-run --auto-servernum --server-args='-screen 0 1920x1080x24' ./downloader"]
