# silent room

Анонимный сайт признаний на Go с PostgreSQL, уникальным счётчиком просмотров и двумя уровнями защиты от спама.

## Запуск

Конфигурация находится в локальном `.env`. Его безопасный шаблон — `.env.example`.

Полный запуск сайта и PostgreSQL в Docker:

```powershell
docker compose up -d --build
```

Либо только база в Docker, а Go-приложение локально:

```powershell
docker compose up -d postgres
go run .
```

Откройте `http://localhost:8080`. При запуске приложение само создаёт таблицы и добавляет демонстрационные признания, только если база пустая.

## Переменные `.env`

- `DATABASE_URL` — строка подключения Go-приложения к PostgreSQL;
- `DATABASE_URL_DOCKER` — подключение контейнера приложения к сервису `postgres`;
- `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_PORT` — настройки базы;
- `IP_HASH_SALT` — соль для необратимого хеширования IP;
- `TRUST_PROXY` — учитывать `X-Forwarded-For` только за доверенным reverse proxy;
- `RATE_LIMIT_REQUESTS`, `RATE_LIMIT_WINDOW`, `RATE_LIMIT_BURST` — общий IP rate limiter для API;
- `SUBMIT_MIN_INTERVAL`, `SUBMIT_DAILY_LIMIT` — ограничения публикаций с одного IP.

Настоящий `.env` исключён из Git. Перед публичным запуском замените пароль PostgreSQL и `IP_HASH_SALT`.

## Хранение и защита

- признания и уникальные просмотры хранятся в PostgreSQL;
- повторный просмотр с одного IP не увеличивает счётчик;
- IP сохраняется только как SHA-256-хеш с солью;
- частые API-запросы получают `429 Too Many Requests` и заголовок `Retry-After`;
- лимит публикаций проверяется внутри транзакции с PostgreSQL advisory lock, поэтому параллельные запросы не обходят ограничение;
- сервер автоматически применяет идемпотентную схему базы;
- текст выводится без интерпретации HTML.

## Проверка

```powershell
go test ./...
go vet ./...
```
