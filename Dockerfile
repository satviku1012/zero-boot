# One image for every ZeroBoot service; docker-compose.yml picks the binary
# each container runs.

# Build stage: compile all four binaries as static Linux executables.
FROM golang:1.26-alpine AS build
WORKDIR /src
# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# Runtime stage: no shell or package manager, just the binaries. The default
# user is root, which the node agent needs to create namespaces and cgroups.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/ /usr/local/bin/
