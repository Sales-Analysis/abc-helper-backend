APP=abc-helper-backend
PKG=.

GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION?=v0.1.0

LDFLAGS=-s -w \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.Version=$(VERSION) \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.Commit=$(GIT_COMMIT) \
	-X github.com/Sales-Analysis/abc-helper-backend/internal/version.BuiltAt=$(BUILD_TIME)

.PHONY: build run test tidy docker clean lint lint-setup swagger swagger-clean

## Сборка бинаря
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(APP) $(PKG)

## Запуск локально
run:
	PORT=8080 go run $(PKG)

## Тесты
test:
	go test ./...

## Приведение зависимостей и кода в порядок
tidy:
	go mod tidy
	go fmt ./...
	go vet ./...

## Сборка Docker-образа
docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(GIT_COMMIT) \
		--build-arg BUILT_AT=$(BUILD_TIME) \
		-t $(APP):latest .

## Очистка артефактов
clean:
	rm -rf bin

## --- linters ---
lint-setup:
	# Установка golangci-lint (Linux/macOS, поменяй версию при желании)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
	  | sh -s -- -b $(shell go env GOPATH)/bin v1.58.2

lint:
	golangci-lint run

## --- swagger ---
swagger:
	# Требуется swag@latest установленный через go install
	swag init --parseDependency --parseInternal \
	  --output ./docs \
	  --generalInfo ./main.go

swagger-clean:
	rm -rf docs
