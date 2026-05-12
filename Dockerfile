# syntax=docker/dockerfile:1

# Backend image: static Go binary on a distroless base. The final image
# carries no shell, no package manager, no libc — only the binary and
# its CA bundle. Resulting size: ~15 MB.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache the module graph: copy go.mod/go.sum first, then download deps.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled + linker flags strip debug info and symbol table.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/gemline ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gemline /gemline
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/gemline"]
