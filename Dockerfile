# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source
COPY *.go ./

# Build static binary
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags='-w -s -extldflags "-static"' \
    -o leader-labeler \
    .

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/leader-labeler /leader-labeler

USER 65532:65532

ENTRYPOINT ["/leader-labeler"]
