FROM golang:1.22-alpine AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .

ARG VERSION=v0.1.0
ARG COMMIT=none
ARG BUILT_AT=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w \
    -X github.com/Sales-Analysis/abc-helper-backend/internal/version.Version=${VERSION} \
    -X github.com/Sales-Analysis/abc-helper-backend/internal/version.Commit=${COMMIT} \
    -X github.com/Sales-Analysis/abc-helper-backend/internal/version.BuiltAt=${BUILT_AT}" \
    -o /out/abc-helper-backend .
RUN mkdir -p /runtime-upload-tmp && chmod 700 /runtime-upload-tmp

FROM scratch
EXPOSE 8080
ENV PORT=8080 READ_TIMEOUT=5s WRITE_TIMEOUT=10s IDLE_TIMEOUT=60s SHUTDOWN_TIMEOUT=10s
ENV ASSISTANT_TIMEOUT=15s
ENV TMPDIR=/upload-tmp
COPY --from=build --chown=10001:10001 /runtime-upload-tmp /upload-tmp
COPY --from=build --chown=10001:10001 /out/abc-helper-backend /abc-helper-backend
USER 10001:10001
ENTRYPOINT ["/abc-helper-backend"]
