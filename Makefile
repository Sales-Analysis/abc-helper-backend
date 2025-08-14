APP=abc-helper-backend
PKG=.

GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS=-s -w \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.Version=v0.1.0 \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.Commit=$(GIT_COMMIT) \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.BuiltAt=$(BUILD_TIME)

.PHONY: build run test tidy docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(APP) $(PKG)

run:
	PORT=8080 go run $(PKG)

test:
	go test ./...

tidy:
	go mod tidy
	go fmt ./...
	go vet ./...

docker:
	docker build -t $(APP):latest .

clean:
	rm -rf bin
