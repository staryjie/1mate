# syntax=docker/dockerfile:1

# The frontend is built here so a plain `docker build .` produces a runnable
# single image without a local Node toolchain.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/../internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
ARG COMMIT=none
RUN go build -trimpath -ldflags "-s -w \
    -X 1mate/internal/version.Version=${VERSION} \
    -X 1mate/internal/version.Commit=${COMMIT}" \
    -o /out/1mate ./cmd/1mate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/1mate /usr/local/bin/1mate
VOLUME ["/data"]
ENV ONEMATE_ADDR=0.0.0.0:8080 \
    ONEMATE_DATA_DIR=/data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/1mate"]
