# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY main.go ./

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -o leader-labeler \
    main.go

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/leader-labeler /leader-labeler

USER 65532:65532

ENTRYPOINT ["/leader-labeler"]
