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

FROM scratch
USER 10001:10001
EXPOSE 8080
ENV PORT=8080 READ_TIMEOUT=5s WRITE_TIMEOUT=10s IDLE_TIMEOUT=60s SHUTDOWN_TIMEOUT=10s
COPY --from=build /out/abc-helper-backend /abc-helper-backend
ENTRYPOINT ["/abc-helper-backend"]
