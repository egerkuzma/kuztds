[English](ARCHITECTURE.md) · **Русский**

# Архитектура KuzTDS

Актуально на 2026-06-07. Снимок прогресса — `docs/STATUS.md`.

## Три бинарника
- `cmd/engine` (:8080) — горячий путь (обработка трафика). Долгоживущий процесс.
- `cmd/admin` (:8090) — REST API + встроенный SPA (`internal/admin/web`, go:embed).
- `cmd/apiclient` (:9090) — клиент для лендинга/донора:
  собирает данные посетителя → зовёт движок `?api=` → применяет ответ.

Общие пакеты — в `internal/`.

## Жизненный цикл запроса в engine

```
HTTP-запрос
  │
  ├─ постбэк? (?pb=KEY&cid=&profit=) → store.RecordPostback, выход
  │
  ├─ realip middleware (XFF/CF только от trusted_proxies)
  │
  ├─ api-режим? (?api=base64(JSON), проверка KUZTDS_API_KEY)
  │     да → вход (ip/ua/ref/lang/uniq/key/domain/cf_country/pars/id) из запроса
  │
  ├─ ipindex.Lookup(ip, ip_blacklist)        → в блэклисте: 403
  │
  ├─ группа по id/алиасу (первый сегмент пути или api.id); нет → trash-режим
  │
  ├─ антифлуд (Redis): N запросов/IP за окно
  │
  ├─ detect.Parse(ua) → device/OS/браузер/бренд ; geo.Resolve(ip) (+CF-IPCountry)
  ├─ ipindex.Lookup(ip, wap) → оператор
  │
  ├─ уникальность: cookie | Redis SETNX
  │
  ├─ router.Select(group, visitor)           → выбор потока по правилам-данным
  │
  ├─ detect ботов ПО ТОГГЛАМ ВЫБРАННОГО ПОТОКА (UA/referer/PTR/empty/ipv6/
  │     ua_blacklist/IP-списки SE/save_ip) → bot_redirect (или skip)
  │
  ├─ separation (ключ→выход из .dat) · remote ([REMOTE], кэш) · chance ·
  │     распределение ||| (random/rotator/evenly) · api_mac
  │
  ├─ render: макросы + тип редиректа (CURL — загрузка+find/replace; api — JSON)
  │
  ├─ save_keys / keys_se (сбор ключевых слов в файлы)
  │
  └─ logbuf.Push(event) → async-батч в ClickHouse (ответ не ждёт)
```

Важно: детект ботов идёт ПОСЛЕ выбора потока — по тогглам
конкретного потока; bot_redirect отдаёт ботам отдельный вывод.

## Пакеты internal/
| Пакет | Роль |
|-------|------|
| `ipindex` | CIDR-индекс O(log n) + менеджер списков с hot-reload |
| `config` | модель групп/потоков (правила-данные) + JSON-загрузчик с алиасами |
| `geo` | Resolver: MMDB (MaxMind) / Nop |
| `detect` | устройство + OS/браузер/бренд (mileusna/useragent) + боты, сигнатуры с hot-reload |
| `router` | выбор потока (предикаты), фильтры lang/country/.../os/browser/brand/schedule/limit |
| `render` | макросы вывода + все типы редиректа |
| `fetch` | HTTP-клиент с TTL-кэшем в памяти (CURL-редирект, `[REMOTE]`) |
| `store` | ClickHouse (логи/постбэки/статистика) + Redis (uniq/limit/firewall/rotate/сессии) |
| `logbuf` | асинхронный буфер событий → батч-вставка в ClickHouse |
| `security` | argon2id, токены/сессии, CSRF, constant-time |
| `server` | realip middleware (trusted proxies) |
| `admin` | HTTP-хендлеры, файловые хранилища (группы/.dat/ключи), встроенный SPA |

## Ключевые принципы
1. **Состояние в памяти процесса** (IP-индексы, конфиг, сигнатуры, гео),
   обновление в фоне — без чтения файлов на запрос.
2. **Горячий путь без лишнего блокирующего I/O**: логи async; счётчики Redis;
   внешние вызовы (CURL/remote/PTR) с таймаутами.
3. **Правила — это данные** (список предикатов, прогоняемых в цикле).
4. **Атомарная замена индексов** при hot-reload (`atomic.Pointer`).

## Конфигурация
- Конфиг групп: JSON-файл `KUZTDS_GROUPS_FILE` (источник истины).
- Engine кэширует группы на старте (после правки файла — перезапуск движка).
- Admin читает/пишет файл при запросах к `/api/groups` (live). Один и тот же
  файл должен быть указан и движку, и админке.
- Секреты/настройки — через переменные окружения `KUZTDS_*` (см. `docs/USAGE.md`).

## Хранилище
- **ClickHouse**: `events` (логи) + `postbacks`. Партиции по дате, TTL для
  авто-очистки (миграции в `migrations/clickhouse`).
- **Redis**: uniq / limit / firewall / rotate (evenly) / сессии админки / rate-limit логина.

## Безопасность (детали — `docs/SECURITY.md`)
realip с доверенными прокси · argon2id + серверные сессии + CSRF · параметризованные
запросы CH · только JSON · секреты вне VCS · экранирование вывода.

## Наблюдаемость (план/частично)
`slog` структурные логи, `/healthz`. `/metrics` (Prometheus) и `pprof` — в TODO.
