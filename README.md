
### Project Structure

```
.
├── cmd/hello/            # входная точка сервиса (main)
│   └── main.go
├── internal/             # приватные пакеты сервиса
│   ├── httpapi/          # маршруты, middleware, handlers
│   ├── service/          # бизнес‑логика (интерфейсы и реализации)
│   ├── config/           # загрузка конфигурации (env, флаги)
│   ├── logx/             # логгер (обёртка над stdlog / slog)
│   └── version/          # версия сборки, build‑info
├── pkg/                  # опционально: публичные утилиты, если появятся
├── Dockerfile
├── go.mod
└── Makefile              # цели сборки/линта/тестов (опционально)
```

### Local build

```bash
make build
./bin/abc-helper-backend

```

### Docker build

```bash
docker build \
  --build-arg VERSION=v0.1.0 \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t abc-helper-backend:latest .

```

### Docker run

```bash
docker run --rm -p 8080:8080 abc-helper-backend:latest
```

## Quality checks before commit

### Pre-commit hook (golangci-lint)

В проекте настроен git hook, который запускает `golangci-lint` **перед каждым коммитом**.  
Коммит будет отклонён, если есть ошибки линтера.

#### Установка hook

```bash
# создать/обновить .git/hooks/pre-commit
mkdir -p .git/hooks
cat > .git/hooks/pre-commit <<'EOF'
#!/bin/sh
echo "🔍 Running pre-commit checks..."

# Линтеры
echo "➡️  Linting..."
if ! make lint; then
  echo "❌ Lint failed. Commit aborted."
  exit 1
fi

# Тесты
echo "➡️  Running tests..."
if ! make test; then
  echo "❌ Tests failed. Commit aborted."
  exit 1
fi

# Swagger docs
echo "➡️  Generating Swagger docs..."
if ! make swagger; then
  echo "❌ Swagger generation failed. Commit aborted."
  exit 1
fi

echo "✅ All pre-commit checks passed!"

EOF

chmod +x .git/hooks/pre-commit
```

## Makefile targets

* make build — сборка бинаря (bin/abc-helper-backend)
* make run — запуск локально (порт 8080)
* make lint — линтеры
* make test — тесты
* make docker — сборка Docker-образа
* make swagger — генерация Swagger-документации (./docs)
* make swagger-clean — очистка docs

## Swagger/OpenAPI

### 1. Сгенерировать спецификацию

```bash
make swagger
```

### 2. Запустить сервис

```bash
make build
./bin/abc-helper-backend
```

### 3. Открыть UI

<http://localhost:8080/swagger/index.html>
