[English](STATUS.md) · **Русский**

# STATUS — где мы и как продолжить

Снимок на 2026-06-07. Для деталей: `docs/USAGE.md`, `TODO.md`.

## Готово (в `main`, тесты зелёные)
- **Фазы 1–7**: ipindex (+hot-reload), realip, geo (mmdb/Nop) + detect
  (устройство/OS/браузер/бренд + боты), router (выбор потока по правилам),
  store/logbuf (ClickHouse + Redis), admin API + встроенный SPA, рендер +
  JSON-конфиг групп.
- **Блок 1** — per-stream бот-тогглы + bot_redirect/out_bot/b_header.
- **Блок 2** — separation, распределение rotator/evenly/random, chance, trash.
- **Блок 3** — CURL-редирект+кэш, remote_pars (`[REMOTE]`), api_mac.
- **Блок 4** — постбэк `?pb=`, сбор ключей (save_keys/keys_se), экраны
  конверсий и ключей.
- **Блок 6** — api-клиент (`cmd/apiclient`) + приём `?api=` движком.
- **Блок 7** — экспорт логов CSV, источники (домены), очистка логов группы,
  перестановка потоков ↑/↓.
- **UI (редизайн 2026-06-07)**: сайдбар-навигация слева, топбар справа (период,
  шестерёнка «Настройки», пользователь, выход), тёмная тема. Дерево групп со
  сворачиванием; клик по потоку — прокрутка+подсветка (фокус на потоке). Полный
  конструктор потока. Покрыт тестами (`web_test.go` + `-tags=uitest` для JS).
- **Фикс**: фильтры country/lang/text работают и когда задан только `values`
  (без `raw`) — `router.go: cfgd()/orJoin()`.
- **Фикс (найдено e2e-тестами 2026-06-07):**
  1. Операторы WAP: `FlagB` теперь белый список конкретных операторов (раньше
     означало «любой оператор присутствует» — нельзя было таргетировать одного).
     `router.go` + комментарий в `config.go`.
  2. Кастомные `ip_list`-файлы потоков теперь подгружаются в `ipindex.Set`
     (`ipListFiles()` в `handler.go`, вызывается в `main.go`) — раньше per-stream
     фильтр по IP молча не срабатывал, если файл не из стандартного набора.

## Тесты (покрытие на 2026-06-07)
Прогон: `go test ./...` (юнит) и `go test -tags=integration ./...` (с CH+Redis).
`go vet ./...` — чисто. Команда покрытия: `go test -tags=integration ./... -cover`.

| Пакет | Покрытие | Заметка |
|-------|:--:|---|
| internal/fetch | 96.9% | httptest + подмена `now` для TTL |
| internal/security | 87.8% | |
| internal/ipindex | 83.7% | |
| internal/geo | 82.6% | mmdb-тест |
| internal/router | 81.9% | + регресс country/lang values-only |
| internal/logbuf | 81.6% | |
| internal/detect | 80.5% | |
| internal/config | 80.0% | |
| internal/store | 77.0% | miniredis (Counters/sessions) + CH под `-tags=integration` |
| internal/admin | 74.1% | login/CSRF/группы/списки/ключи/пароль/экспорт + file-сторы + SPA (web_test.go) |
| internal/server | 73.2% | |
| internal/render | 76.8% | |
| cmd/apiclient | 71.6% | round-trip с фейковым TDS (`newClientHandler`) |
| cmd/engine | 67.1% | httptest-конвейер + хелперы + **e2e_test.go** (23 сквозных сценария: все типы редиректа, все макросы, боты, гео, фильтры, операторы, распределение, лимиты, фаервол, separation, расписание, chance, api-режим, матрица трафика) |
| cmd/admin | 0% | только `main()`-обвязка; логика в internal/admin |

Рефактор для тестируемости: обработчики горячего пути вынесены из замыканий
`main()` в `cmd/engine/handler.go` (`engineDeps.root`) и `cmd/apiclient`
(`newClientHandler`). ClickHouse-тесты — под build-tag `integration`
(`go test -tags=integration ./internal/store/`), пропускаются при недоступном CH.

## Осталось
- **Блок 5 — cron** (НЕ начат): обновление IP-списков ботов, VirusTotal,
  мониторинг диска + Telegram, очистка.
- **Фичи 1.0.5** (см. `TODO.md`): ASN/организации/таймзоны (гео), GET-фильтр,
  «Другие устройства», eval-редирект, Telegram-уведомления о конверсиях, версии
  в OS/browser-фильтрах `windows:7;10`, клонирование групп.

## Как запустить заново (дев)
```bash
cd kuztds
make infra-up                         # ClickHouse + Redis
go run ./cmd/admin -hash 'admin123' > /tmp/admin.hash   # хэш пароля

# движок :8080
KUZTDS_DATA_DIR=../database KUZTDS_GROUPS_FILE=configs/test_groups.json \
KUZTDS_TRUSTED_PROXIES=127.0.0.1/32 KUZTDS_POSTBACK_KEY=pbsecret KUZTDS_API_KEY=apikey123 \
KUZTDS_KEYS_DIR=/tmp/kuztds-keys KUZTDS_GEO_DB=internal/geo/testdata/GeoLite2-City-Test.mmdb \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine

# админка :8090 (admin / admin123)
KUZTDS_ADMIN_PASSWORD_HASH="$(cat /tmp/admin.hash)" KUZTDS_ADMIN_PASSWORD_FILE=/tmp/admin.hash \
KUZTDS_ADMIN_COOKIE_SECURE=false KUZTDS_ENGINE_URL=http://localhost:8080 \
KUZTDS_GROUPS_FILE=configs/test_groups.json KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=/tmp/kuztds-keys \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```
Схема ClickHouse применяется автоматически из `migrations/clickhouse/*.sql`.
Для существующей БД миграции 002/003 применять через
`docker exec -i deploy-clickhouse-1 clickhouse-client ... --multiquery < файл`.

## Заметки
- Админка и движок должны указывать на ОДИН `KUZTDS_GROUPS_FILE`; движок кэширует
  группы на старте (после правки файла — перезапуск движка; админка читает live).
- Группа: `id` = идентификатор и адрес `/<id>`; `name` — опциональное отображаемое имя.
- Удалённого git нет — пуш делает пользователь сам.
