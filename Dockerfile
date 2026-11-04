# Builds one of the commands in cmd/: the API by default, or for example
# --build-arg CMD=webhook-receiver.
ARG CMD=api

# Build stage: compile a static binary with the full Go toolchain.
FROM golang:1.26-alpine AS build
ARG CMD
WORKDIR /src

# Download dependencies first: this layer is cached until go.mod changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

# Run stage: only the binary, on a minimal image without a shell, as a
# non-root user. The final image weighs a few megabytes.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
