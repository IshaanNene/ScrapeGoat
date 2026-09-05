# The builder must be at least the version in go.mod's `go` directive. It was
# pinned to 1.24 against a go1.25.0 module, which made every image build silently
# download a second toolchain before it could compile anything.
FROM golang:1.27-alpine AS builder

# hadolint ignore=DL3018
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/IshaanNene/ScrapeGoat/internal/config.Version=${VERSION}" \
    -o /scrapegoat ./cmd/scrapegoat

FROM alpine:3.22

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates tzdata chromium

# This process drives headless Chromium against pages it does not control. Running
# that as root means a Chromium sandbox escape lands as root in the container.
RUN addgroup -S scrapegoat && adduser -S -G scrapegoat -h /home/scrapegoat scrapegoat

COPY --from=builder /scrapegoat /usr/local/bin/scrapegoat

# Output and job databases are written relative to the working directory, so it has
# to be somewhere the unprivileged user can write.
RUN mkdir -p /data && chown scrapegoat:scrapegoat /data
WORKDIR /data
VOLUME ["/data"]

ENV ROD_BROWSER=/usr/bin/chromium-browser

USER scrapegoat

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["scrapegoat", "version"]

ENTRYPOINT ["scrapegoat"]
