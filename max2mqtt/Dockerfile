# Build Stage: standard Go Alpine image, builds a static binary
FROM golang:1.26-alpine AS builder

# Arguments for cross-compilation (passed automatically by docker buildx / HA)
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build static binary for the target architecture
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o max2mqtt .

# Final Stage
# A fixed FROM, not ARG BUILD_FROM: a Supervisor before 2026.04.0 passes its own
# BUILD_FROM (ghcr.io/home-assistant/base) when there is no build.yaml, which
# would override an ARG default. Since 2026.04.0 the Supervisor passes none.
FROM alpine:3.24

# Add Home Assistant Labels
# These help the supervisor identify the add-on
LABEL \
    io.hass.name="MAX! to MQTT Bridge" \
    io.hass.description="Bridge for MAX! Wall Thermostats via CUL-Stick to MQTT" \
    io.hass.arch="aarch64|amd64" \
    io.hass.type="addon" \
    io.hass.version="1.9.2"

WORKDIR /app

# Install runtime dependencies. The Supervisor builds the image on the user's
# device: retry after 20 s and 2 min, so a short network outage does not make
# the update fail.
RUN for wait in 20 120 0; do \
        apk add --no-cache tzdata ca-certificates && break; \
        [ "${wait}" -eq 0 ] && exit 1; \
        echo "apk failed, retrying in ${wait} s"; sleep "${wait}"; \
    done

# Copy binary from builder
COPY --from=builder /app/max2mqtt /app/max2mqtt
COPY run.sh /app/run.sh

# Strip Windows CRLF if present, ensure executable, create data dir
RUN sed -i 's/\r$//' /app/run.sh && \
    chmod +x /app/run.sh && \
    mkdir -p /data

ENTRYPOINT ["/app/run.sh"]
