
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
echo "🔍 Running golangci-lint before commit..."
golangci-lint run
if [ $? -ne 0 ]; then
    echo "❌ Lint failed — commit aborted."
    exit 1
fi
echo "✅ Lint passed."
EOF

chmod +x .git/hooks/pre-commit