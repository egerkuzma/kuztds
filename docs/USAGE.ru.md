[English](USAGE.md) · **Русский**

# KuzTDS — руководство

Что это, как запустить, как настроить.

> Текущее состояние проекта и команды для возобновления — в **`docs/STATUS.md`**.
> Что осталось — `TODO.md`.

## Что это

Система распределения трафика (TDS): принимает посетителя, по правилам решает,
куда его направить (редирект/iframe/JS/контент/заглушка), отделяет ботов от
людей, пишет статистику. Написана на Go ради скорости и безопасности.

Три исполняемых компонента:

| Бинарник | Назначение | Порт по умолчанию |
|----------|-----------|-------------------|
| `cmd/engine` | движок (горячий путь — обработка трафика) | 8080 |
| `cmd/admin` | REST API + веб-интерфейс администратора | 8090 |
| `cmd/apiclient` | клиент для лендинга/донора | 9090 |

Хранилища: **ClickHouse** (логи/конверсии), **Redis** (уникальность/лимиты/
фаервол/сессии). Оба опциональны — без них движок работает, пропуская
соответствующие проверки.

## Быстрый старт

```bash
# зависимости
brew install go docker
cd kuztds

# инфраструктура (ClickHouse + Redis), схема применяется автоматически
make infra-up

# хэш пароля админки
go run ./cmd/admin -hash 'мой-пароль'   # выведет $argon2id$...

# движок
KUZTDS_DATA_DIR=../database KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_TRUSTED_PROXIES=127.0.0.1/32 KUZTDS_POSTBACK_KEY=secret KUZTDS_API_KEY=apikey \
KUZTDS_REDIS_ADDR=localhost:6379 \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_DB=kuztds \
KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine

# админка (открыть http://localhost:8090, логин admin)
KUZTDS_ADMIN_PASSWORD_HASH='<хэш>' KUZTDS_ADMIN_PASSWORD_FILE=./admin.hash \
KUZTDS_ENGINE_URL=http://localhost:8080 KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=./keys \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```

## Переменные окружения

### engine
| Переменная | Назначение |
|------------|-----------|
| `KUZTDS_LISTEN` | адрес прослушивания (`:8080`) |
| `KUZTDS_DATA_DIR` | каталог `.dat`-списков (IP, wap, сигнатуры) |
| `KUZTDS_GROUPS_FILE` | JSON-конфиг групп (без него — встроенная demo) |
| `KUZTDS_TRUSTED_PROXIES` | CIDR доверенных прокси (для XFF/CF), через запятую |
| `KUZTDS_GEO_DB` | путь к MaxMind `.mmdb` (без него гео по CF-IPCountry). Для теста есть `internal/geo/testdata/GeoLite2-City-Test.mmdb` |
| `KUZTDS_REDIS_ADDR` / `KUZTDS_REDIS_PASSWORD` | Redis (uniq/limit/firewall) |
| `KUZTDS_CLICKHOUSE_ADDR` / `_DB` / `_USER` / `_PASSWORD` | ClickHouse (логи) |
| `KUZTDS_POSTBACK_KEY` | ключ для `?pb=` постбэка |
| `KUZTDS_API_KEY` | ключ для режима `?api=` (api-клиенты) |
| `KUZTDS_KEYS_DIR` | каталог сбора ключевых слов |
| `KUZTDS_TRASH_MODE` / `KUZTDS_TRASH_URL` | поведение для неизвестной группы (0=200,1=redirect,2=403,3=404) |
| `KUZTDS_CURL_CACHE` | кэш CURL-редиректа, минут |
| `KUZTDS_RELOAD_INTERVAL` | период hot-reload `.dat` (`1m`) |

### admin
| Переменная | Назначение |
|------------|-----------|
| `KUZTDS_ADMIN_LISTEN` | адрес (`:8090`) |
| `KUZTDS_ADMIN_USER` | логин (`admin`) |
| `KUZTDS_ADMIN_PASSWORD_HASH` | argon2id-хэш пароля |
| `KUZTDS_ADMIN_PASSWORD_FILE` | файл хэша (приоритетнее; туда пишется смена пароля в UI) |
| `KUZTDS_ADMIN_COOKIE_SECURE` | флаг Secure для cookie (`true`; локально `false`) |
| `KUZTDS_ENGINE_URL` | базовый URL движка (для ссылок групп в UI) |
| `KUZTDS_GROUPS_FILE` / `KUZTDS_DATA_DIR` / `KUZTDS_KEYS_DIR` | те же пути, что у движка |
| `KUZTDS_REDIS_ADDR` / `KUZTDS_CLICKHOUSE_*` | хранилища (сессии, статистика) |

### apiclient
`KUZTDS_TDS_URL` (URL движка), `KUZTDS_API_KEY`, `KUZTDS_GROUP_ID`,
`KUZTDS_APICLIENT_LISTEN`, `KUZTDS_TRUSTED_PROXIES`.

## Ссылки, которые отдаёт движок

- Группа: `http://домен/<id>` (и алиасы `http://домен/<alias>`)
- С ключевым словом: `http://домен/<id>?q=КЛЮЧ`
- С доп. параметрами: `http://домен/<id>?p1=...&p2=...` → макросы `[PAR-1..5]`
- Постбэк-пиксель: `http://домен/?pb=KEY&cid=[CID]&profit=1.50`

Группа ищется по **всему пути**, а не по первому сегменту: `/promo` и `/promo/`
попадают в группу `promo`, а `/promo/landing` не совпадает ни с чем и уходит в
trash-режим. Всё остальное передавайте в query-строке.

## Веб-интерфейс админки

Оболочка: **сайдбар слева** (навигация с иконками), **топбар справа** (выбор
периода, шестерёнка «Настройки», чип пользователя и кнопка выхода). Тёмная тема.

Разделы: **Дашборд** (график с заливкой + разбивки страны/устройства/ОС/браузеры/
бренды/группы/источники), **Логи** (фильтры-**выпадающие списки с галочками**:
группа/поток/страна/устройство/OS/браузер/бренд — значения подгружаются из данных
за период `GET /api/logs/filters`, можно отметить несколько → SQL `IN`; плюс
поиск внутри списка, IP-поле, тип люди/боты, пагинация, экспорт CSV),
**Конверсии**, **Ключи** (просмотр собранных ключевых слов), **Группы** (дерево
групп→потоки со сворачиванием, конструктор форм потока), **Списки** (редактор
`.dat`, в т.ч. WAP-операторы). «Настройки» (смена пароля) — по шестерёнке справа.

Группы: дерево слева со **сворачиванием/разворачиванием** (шеврон у группы), при
**клике на поток** форма прокручивается к нему с подсветкой (фокус на потоке).
Форма потока (вкладки): Главное · Устройства · WAP · Гео ·
Фильтры · UA/ОС/Бренд · Расписание · Лимит · Боты · Remote · API.

UI — один встроенный файл (`internal/admin/web/index.html`, `go:embed`). Тесты:
`internal/admin/web_test.go` (отдача SPA + структурные якоря + ключевые функции),
а проверка синтаксиса встроенного JS — `go test -tags=uitest ./internal/admin/`
(нужен node; одна опечатка в скрипте обрушивает весь SPA — этот тест её ловит).

Семантика флагов фильтров: **выключено / исключать (чёрный список) / отбирать
(белый список)**.

## Конфиг групп (JSON)

Примеры: `configs/groups.example.json`, `configs/test_groups.json` (набор разных
групп для прогона трафика). Структура: массив групп `{id, name, status, redirect,
header, out, geo, uniq_method, uniq_seconds, firewall, save_keys, save_keys_se,
aliases, streams[]}`; поток `{name, status, rules, out{redirect,out,chance,
distribution}, bots, separation, remote, api_mac, curl}`.

- `id` — идентификатор и адрес `/<id>`; `name` — опциональное отображаемое имя.
- Дефолты группы (`redirect`/`out`/`header`) применяются, если поток не задал своё.
- Редактируется через UI («Группы»: дерево со сворачиванием, конструктор потока,
  «Сохранить всё»). Движок кэширует группы на старте — после правки файла
  напрямую нужен его перезапуск; правки из UI движок подхватит при reload/рестарте.

## Макросы вывода

`[KEY] [PATH] [IP] [COUNTRY] [CITY] [REGION] [LANG] [DEVICE] [OPERATOR]
[DOMAIN] [USERAGENT] [CID] [PAR-1..5] [()COUNTRY()] [()CITY()]
[RANDNUM-a-b] [RANDSTR-(набор)-n] [RANDLINE-(файл)-n[/u]] [RANDDFL-(каталог)-n[/u]]`

## Особенности реализации

- Один долгоживущий процесс; IP-списки в памяти (поиск O(log n)).
- ClickHouse для логов/конверсий; Redis-счётчики для уникальности/лимитов/фаервола.
- Безопасность: argon2id, серверные сессии, CSRF, параметризованные запросы,
  только JSON, доверенные прокси для XFF.
- Уникальность «по cookie»: выделенная cookie с корректным TTL.

## Планы
См. `TODO.md` (главное — cron-сервис: обновление IP-списков, VirusTotal,
мониторинг диска/Telegram).
