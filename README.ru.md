[English](README.md) · **Русский**

# KuzTDS

**Быстрая, безопасная, self-hosted система распределения трафика (TDS) на Go.**

KuzTDS принимает входящего посетителя, **по правилам** решает, куда его направить
(редирект / iframe / JavaScript / контент / заглушка), отделяет **ботов от людей**
и пишет всё для аналитики — из одного долгоживущего бинарника со встроенной
админ-панелью.

Сделан ради скорости и безопасности: IP-списки и сигнатуры держатся в памяти
(поиск `O(log n)`), логи и конверсии идут в **ClickHouse**, счётчики и сессии — в
**Redis**, а горячий путь никогда не блокируется на дисковом I/O.

![Дашборд](docs/img/dashboard.png)

---

## Содержание

- [Зачем KuzTDS](#зачем-kuztds)
- [Возможности](#возможности)
- [Скриншоты](#скриншоты)
- [Архитектура](#архитектура)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация)
- [Основные понятия](#основные-понятия)
- [Фильтры и семантика флагов](#фильтры-и-семантика-флагов)
- [Вывод: типы редиректа и макросы](#вывод-типы-редиректа-и-макросы)
- [Детект ботов](#детект-ботов)
- [Постбэк и API-режим](#постбэк-и-api-режим)
- [Веб-интерфейс админки](#веб-интерфейс-админки)
- [HTTP-эндпоинты](#http-эндпоинты)
- [Тестирование](#тестирование)
- [Структура проекта](#структура-проекта)
- [Безопасность](#безопасность)
- [Планы](#планы)
- [Лицензия](#лицензия)

---

## Зачем KuzTDS

TDS стоит перед твоими офферами/лендингами и направляет каждый клик в нужное
место в зависимости от того, кто пришёл. KuzTDS:

- **Быстрый** — один процесс, индексы в памяти, асинхронное логирование. Без
  чтения файлов и сканов БД на каждый запрос.
- **Безопасный** — argon2id-пароли, серверные сессии, CSRF, параметризованные
  запросы, доверенные прокси для `X-Forwarded-For`, только JSON на входе.
- **Самодостаточный** — админка (SPA) встроена в бинарник `admin` через
  `go:embed`. Без сборки на Node и без отдельного веб-сервера.
- **Удобный в эксплуатации** — ClickHouse и Redis **опциональны**: без них движок
  работает и просто пропускает соответствующие проверки (логи, счётчики).

---

## Возможности

**Сегментация трафика (по потокам):**
- Гео: страна / город / регион (MaxMind `.mmdb` или Cloudflare `CF-IPCountry`).
- Устройство: компьютер / телефон / планшет.
- ОС, браузер (+ версии), бренд устройства (Apple, Samsung, Xiaomi, …).
- WAP-оператор по диапазонам IP из `wap.dat`.
- Язык, реферер, домен, ключевое слово (`?q=`), произвольные IP-списки.
- Яндекс.Браузер, уникальность, наличие реферера.
- Расписание по дням недели.
- Лимиты показов потока (в сутки / за скользящее окно).

**Работа с ботами:**
- Сигнатуры UA/referer, IP-списки поисковиков (Google/Bing/Yandex/Yahoo/Mail/
  Baidu/Others), пустые UA/referer/язык, IPv6, обратный DNS (PTR), UA-блэклист.
- Боты определяются **после** выбора потока (по тогглам выбранного потока).
- Опциональная отдельная отдача ботам (`bot_redirect`) или `skip` — отдать им
  обычный поток, но залогировать как бота.
- `save_ip`: дописать найденный IP поисковика обратно в его список.

**Вывод и рендер:**
- 16 типов редиректа (HTTP-редирект, JS, meta refresh, iframe, инлайн-HTML,
  страницы ошибок, JSON-ответы для API, заглушка, …).
- Богатый набор макросов: `[KEY] [IP] [COUNTRY] [CITY] [REGION] [LANG] [DEVICE]
  [OPERATOR] [DOMAIN] [USERAGENT] [CID] [PAR-1..5] [()COUNTRY()] [()CITY()]
  [RANDNUM-a-b] [RANDSTR-(set)-n] [RANDLINE-(file)-n] [RANDDFL-(dir)-n]`.
- Распределение вариантов через `|||`: `random` / `rotator` (cookie) / `evenly`
  (Redis-счётчик).
- `chance` (показ с вероятностью), `separation` (подмена вывода по ключу из
  `.dat`), `[REMOTE]` (подгрузка внешнего контента с кэшем), CURL-редирект
  (загрузка + find/replace), `api_mac` (mac-код в ответах API).

**Уникальность и защита:**
- Уникальность по IP (Redis) или по cookie (выделенная cookie, корректный TTL).
- Антифлуд (макс. запросов с IP за окно).
- Rate-limit логина (Redis sliding window).

**Аналитика и админка:**
- Дашборд с графиком и разбивками (страна/устройство/ОС/браузер/бренд/группа/
  источник).
- Логи с мультиселект-фильтрами по реальным данным, поиском по IP, экспортом CSV
  и флагами стран.
- Конверсии (постбэки), собранные ключевые слова, конструктор групп/потоков,
  редактор `.dat`-списков, смена пароля.

---

## Скриншоты

**Логи** — мультиселект-фильтры (значения подгружаются из данных за выбранный
период), поиск внутри списка, флаги стран, экспорт CSV, пагинация:

![Логи](docs/img/logs.png)

**Группы и потоки** — сворачиваемое дерево групп, конструктор формы потока с
вкладками, живые ссылки, которые отдаёт движок, и фокус на потоке при клике:

![Группы и потоки](docs/img/groups.png)

---

## Архитектура

Три бинарника, общие пакеты в `internal/`:

| Бинарник | Порт | Назначение |
|----------|------|------------|
| `cmd/engine` | `:8080` | Горячий путь — обработка трафика. Долгоживущий процесс. |
| `cmd/admin` | `:8090` | REST API + встроенный SPA админки (`internal/admin/web`, `go:embed`). |
| `cmd/apiclient` | `:9090` | Клиент для установки на лендинг/донор (зовёт движок через `?api=`). |

**Жизненный цикл запроса в движке:**

```
HTTP-запрос
  │
  ├─ постбэк?  (?pb=KEY&cid=&profit=) → записать конверсию, выход
  ├─ realip middleware  (доверять XFF/CF только от trusted_proxies)
  ├─ api-режим?  (?api=base64(JSON), проверка KUZTDS_API_KEY)
  ├─ IP-блэклист        → 403, если в списке
  ├─ группа по id/алиасу (первый сегмент пути); нет → режим «trash»
  ├─ антифлуд (Redis): N запросов / IP / окно
  ├─ детект устройства/ОС/браузера/бренда ; гео (mmdb / CF-IPCountry) ; оператор (wap)
  ├─ уникальность: cookie | Redis SETNX
  ├─ router.Select(group, visitor)  → первый поток, прошедший все фильтры
  ├─ детект ботов по тогглам ВЫБРАННОГО потока → bot_redirect (или skip)
  ├─ separation · [REMOTE] · chance · распределение (|||) · api_mac
  ├─ рендер: макросы + тип редиректа (CURL = загрузка+find/replace; api = JSON)
  ├─ сбор ключевых слов (save_keys / keys_se)
  └─ async-батч лог → ClickHouse  (ответ никогда не ждёт записи)
```

Ключевые принципы: **состояние в памяти процесса** (IP-индексы, конфиг,
сигнатуры, гео), обновляется в фоне; **правила — это данные** (список предикатов
в цикле); индексы при hot-reload меняются **атомарно**. Подробнее:
[`docs/ARCHITECTURE.ru.md`](docs/ARCHITECTURE.ru.md).

---

## Быстрый старт

### Требования
- Go (1.25+), Docker (для ClickHouse + Redis).

```bash
brew install go docker
```

### 1. Клонировать и поднять инфраструктуру
```bash
git clone https://github.com/egerkuzma/kuztds.git
cd kuztds
make infra-up          # ClickHouse + Redis через docker compose; схема применяется автоматически
```

### 2. Сгенерировать хэш пароля админки
```bash
go run ./cmd/admin -hash 'мой-надёжный-пароль'   # выведет $argon2id$...
```

### 3. Запустить движок (`:8080`)
```bash
KUZTDS_DATA_DIR=./data \
KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_TRUSTED_PROXIES=127.0.0.1/32 \
KUZTDS_POSTBACK_KEY=secret KUZTDS_API_KEY=apikey \
KUZTDS_REDIS_ADDR=localhost:6379 \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_DB=kuztds \
KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine
```

### 4. Запустить админку (`:8090`)
```bash
KUZTDS_ADMIN_PASSWORD_HASH='<хэш-из-шага-2>' \
KUZTDS_ADMIN_PASSWORD_FILE=./admin.hash \
KUZTDS_ADMIN_COOKIE_SECURE=false \
KUZTDS_ENGINE_URL=http://localhost:8080 \
KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_DATA_DIR=./data KUZTDS_KEYS_DIR=./keys \
KUZTDS_REDIS_ADDR=localhost:6379 \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_DB=kuztds \
KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```

Открой **http://localhost:8090** и войди как `admin` со своим паролем.

### 5. Отправить тестовый запрос на движок
```bash
curl -i 'http://localhost:8080/promo?q=hello' \
  -H 'X-Forwarded-For: 8.8.8.8' \
  -H 'User-Agent: Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) Safari/604.1' \
  -H 'CF-IPCountry: US'
```
Диагностические заголовки ответа показывают решение: `X-Kuztds-Stream`,
`X-Kuztds-Bot`, `X-Kuztds-Country`, `X-Kuztds-Device`, `X-Kuztds-Uniq`.

> Есть `Makefile`: `make build`, `make test`, `make bench`, `make infra-up`,
> `make infra-down`.

---

## Конфигурация

Всё настраивается через переменные окружения `KUZTDS_*`; **секреты никогда не
пишутся в файлы в репозитории**. Полный справочник: [`docs/USAGE.ru.md`](docs/USAGE.ru.md).

### Движок (основное)
| Переменная | Назначение |
|------------|-----------|
| `KUZTDS_LISTEN` | адрес прослушивания (по умолчанию `:8080`) |
| `KUZTDS_DATA_DIR` | каталог `.dat`-списков (IP, wap, сигнатуры, separation) |
| `KUZTDS_GROUPS_FILE` | JSON-конфиг групп (источник истины) |
| `KUZTDS_TRUSTED_PROXIES` | CIDR доверенных прокси для `XFF`/`CF` (через запятую) |
| `KUZTDS_GEO_DB` | путь к MaxMind `.mmdb` (опц.; иначе `CF-IPCountry`) |
| `KUZTDS_REDIS_ADDR` / `_PASSWORD` | Redis (uniq/limit/firewall) |
| `KUZTDS_CLICKHOUSE_ADDR` / `_DB` / `_USER` / `_PASSWORD` | ClickHouse (логи) |
| `KUZTDS_POSTBACK_KEY` | ключ для постбэка `?pb=` |
| `KUZTDS_API_KEY` | ключ для api-режима `?api=` |
| `KUZTDS_KEYS_DIR` | каталог собранных ключевых слов |
| `KUZTDS_TRASH_MODE` / `_URL` | поведение для неизвестной группы (0=200,1=redirect,2=403,3=404) |

### Админка (основное)
| Переменная | Назначение |
|------------|-----------|
| `KUZTDS_ADMIN_LISTEN` | адрес (по умолчанию `:8090`) |
| `KUZTDS_ADMIN_USER` | логин (по умолчанию `admin`) |
| `KUZTDS_ADMIN_PASSWORD_HASH` | argon2id-хэш |
| `KUZTDS_ADMIN_PASSWORD_FILE` | файл хэша (приоритетнее; смена пароля в UI пишется сюда) |
| `KUZTDS_ENGINE_URL` | базовый URL движка (для ссылок групп в UI) |
| `KUZTDS_GROUPS_FILE` / `KUZTDS_DATA_DIR` / `KUZTDS_KEYS_DIR` | те же пути, что у движка |

### Конфиг групп (JSON)
Группа доступна по `/<id>` (и по каждому алиасу). Пример структуры:

```json
[
  {
    "id": "promo",
    "name": "Promo (RU/US segmentation)",
    "status": true,
    "redirect": "show_text",
    "out": "FALLBACK",
    "uniq_method": "cookie",
    "uniq_seconds": 86400,
    "firewall": { "enabled": false, "queries": 100, "seconds": 60 },
    "save_keys": true,
    "aliases": ["ru"],
    "streams": [
      {
        "name": "ru_mobile",
        "status": true,
        "rules": { "country": { "flag": 2, "values": ["ru"] }, "computer": 1, "tablet": 1 },
        "out": { "redirect": "http_redirect", "out": "https://m.example.com/?k=[KEY]&c=[COUNTRY]" },
        "bots": { "ch_ua": true, "ch_empty_ua": true, "ch_bot_ip_google": true, "redirect": "404_not_found" }
      },
      { "name": "default", "status": true, "out": { "redirect": "show_text", "out": "no offer" } }
    ]
  }
]
```

Обычно группы **редактируются в админке** («Группы» → конструктор → «Save all»);
движок кэширует группы на старте и перечитывает их по reload-интервалу. См.
`configs/groups.example.json` и `configs/test_groups.json`.

---

## Основные понятия

- **Группа** — цель роутинга по `/<id>`. Хранит дефолты (`redirect`/`out`/
  `header`), настройки уникальности/фаервола и список **потоков**.
- **Поток** — набор **правил** (предикатов) плюс **вывод**. Роутер выбирает
  **первый активный поток, прошедший все свои правила**; порядок важен.
- **Правила — это данные** — каждый фильтр это значение с флагом; роутер
  прогоняет их в цикле. Если ни один поток не подошёл — применяются дефолты
  группы / режим `trash`.
- **Режим trash** — что вернуть для неизвестной/выключенной группы: пустой `200`,
  редирект, `403` или `404`.

---

## Фильтры и семантика флагов

Большинство списочных фильтров — трёхзначный флаг (нулевое значение = «выключено»,
поэтому незаданные фильтры никогда не режут трафик):

| Флаг | Списочные фильтры | Устройства/операторы |
|:----:|-------------------|----------------------|
| `0` | **выключено** — фильтр не применяется | — |
| `1` | **исключать** — отказ при совпадении (чёрный список) | блокировать |
| `2` | **отбирать** — отказ при отсутствии совпадения (белый список) | требовать (белый список) |

В UI это **выключено / исключать / отбирать**. `lang`/`country` — семантика
«содержит»; `city`/`region`/`brand` — точное совпадение; `ua`/`referer`/`key` —
`/regex/` или подстрока; `os`/`browser` — по `"имя версия"`.

---

## Вывод: типы редиректа и макросы

**Типы редиректа** (`out.redirect`):

| Тип | Результат |
|------|-----------|
| `http_redirect` | `302` с `Location: <out>` |
| `meta_refresh`, `js_redirect`, `iframe_redirect`, `iframe_selection`, `js_selection` | HTML, который уводит браузер |
| `javascript` | `200` JS-тело |
| `show_text` | `200` текст |
| `show_page_html` | `200` HTML-страница вокруг `<out>` |
| `under_construction` | `200` страница-заглушка |
| `stop` | `200`, пустое тело |
| `403_forbidden`, `404_not_found`, `500_server_error` | страницы ошибок |
| `api` | `200` JSON `{out,type,country,device,…}` (для api-клиентов) |
| `show_out` | `200` JSON `{out,type:1,mac}` |
| `curl` | загрузить URL на сервере, применить find/replace, вернуть тело |

**Макросы** (раскрываются в `out`):

`[KEY]` (url-кодированный ключ) · `[PATH]` (хост) · `[IP]` · `[COUNTRY]` `[CITY]`
`[REGION]` · `[LANG]` · `[DEVICE]` · `[OPERATOR]` · `[DOMAIN]` · `[USERAGENT]` ·
`[CID]` (click id, для постбэков) · `[PAR-1..5]` (доп. GET-параметры) ·
`[()COUNTRY()]` `[()CITY()]` · `[RANDNUM-a-b]` · `[RANDSTR-(charset)-n]` ·
`[RANDLINE-(file)-n[/u]]` · `[RANDDFL-(dir)-n[/u]]`.

**Распределение** — положи несколько вариантов в `out` через `|||` и выбери
`distribution`: `random`, `rotator` (липкий по cookie) или `evenly` (Redis-
счётчик, по кругу).

---

## Детект ботов

Для каждого потока включаешь нужные проверки (вкладка **Bots** в редакторе):
сигнатуры UA/referer, пустые UA/referer/язык, IPv6, PTR (обратный DNS),
UA-блэклист и IP-списки поисковиков. Детект идёт **после** выбора потока. Дальше:

- `redirect: "skip"` (или пусто) — отдать вывод обычного потока, но всё равно
  залогировать как бота.
- `redirect: "404_not_found"` (или любой тип) + опц. `out`/`header` — отдать
  ботам **отдельный** вывод (`bot_redirect`).
- `save_ip: true` — дописать найденный IP поисковика обратно в его `.dat`.

---

## Постбэк и API-режим

**Постбэк (трекинг конверсий).** Положи click id в URL оффера через макрос
`[CID]`, и пусть оффер вызовет:

```
http://your-host/?pb=YOUR_POSTBACK_KEY&cid=[CID]&profit=1.50
```
Движок найдёт исходное событие по `cid` и запишет конверсию (видна в разделе
**Conversions**).

**API-режим.** Лендинг может запросить решение у движка, не редиректя посетителя
сам: отправить `?api=base64(JSON)` (с ключом `KUZTDS_API_KEY`) с данными
посетителя; движок вернёт JSON-решение. `cmd/apiclient` — готовый клиент, который
собирает данные посетителя, зовёт движок и применяет ответ (редирект или контент).

---

## Веб-интерфейс админки

Один встроенный SPA (тёмная тема, английский UI). Раскладка: **сайдбар слева**,
**справа сверху** выбор периода, шестерёнка настроек, пользователь и выход.

- **Dashboard** — карточки визиты/уникальные/боты, график по времени и разбивки
  по странам, устройствам, ОС, браузерам, брендам, группам и источникам.
- **Logs** — мультиселект-фильтры (группа/поток/страна/устройство/ОС/браузер/
  бренд), значения которых подгружаются из данных за период, поиск внутри списка,
  поле IP, переключатель люди/боты, флаги стран, пагинация, экспорт CSV.
- **Conversions** — постбэки и суммарный профит за период.
- **Keywords** — собранные ключевые слова по группе/дате.
- **Groups** — сворачиваемое дерево группа→поток; конструктор формы потока с
  вкладками (Main · Devices · WAP · Geo · Filters · UA/OS/Brand · Schedule ·
  Limit · Bots · Remote · API); клик по потоку прокручивает к его форме и
  подсвечивает её.
- **Lists** — редактор `.dat`-файлов (IP-базы, WAP-операторы, сигнатуры).
- **Settings** (шестерёнка) — смена пароля админки.

---

## HTTP-эндпоинты

**Движок:**
- `GET /<id>` — отдать группу (опц. `?q=<keyword>`, `?p1=..&p2=..`).
- `GET /?pb=KEY&cid=..&profit=..` — постбэк-пиксель.
- `GET /?api=base64(JSON)` — режим api-клиента.
- `GET /healthz` — health-проба.

**Admin API** (за сессией + CSRF): `POST /api/login`, `POST /api/logout`,
`GET /api/me`, `POST /api/password`, `GET /api/stats/{summary,timeseries,
breakdown}`, `GET /api/logs`, `GET /api/logs/filters`, `GET /api/logs/export`,
`DELETE /api/logs`, `GET /api/postbacks`, `GET /api/keys`,
`GET|PUT /api/groups`, `GET /api/lists`, `GET|PUT /api/lists/{name}`.

---

## Тестирование

```bash
go test ./...                              # юнит-тесты (14 пакетов)
go test -tags=integration ./...            # + round-trip ClickHouse/Redis (нужен make infra-up)
go test -tags=uitest ./internal/admin/     # проверяет, что JS встроенного SPA парсится (нужен node)
go vet ./...
make bench                                 # бенч ipindex (~10 нс/lookup)
```

Особенность: `cmd/engine/e2e_test.go` прогоняет **23 сквозных сценария** через
полный конвейер (все типы редиректа, все макросы, боты, гео, фильтры, операторы,
распределение, лимиты, фаервол, separation, расписание, chance, api-режим и
матрица трафика). ClickHouse-тесты за build-тегом `integration` и автоматически
скипаются при недоступном ClickHouse. Снимок покрытия:
[`docs/STATUS.ru.md`](docs/STATUS.ru.md).

---

## Структура проекта

```
cmd/
  engine/       горячий путь (обработка трафика)
  admin/        REST API + встроенный SPA
  apiclient/    клиент для лендинга/донора
internal/
  ipindex/      CIDR-индекс O(log n) + менеджер списков с hot-reload
  config/       модель групп/потоков + JSON-загрузчик
  geo/          MMDB (MaxMind) / Nop резолвер
  detect/       устройство + ОС/браузер/бренд + боты, сигнатуры
  router/       выбор потока (предикаты)
  render/       макросы вывода + все типы редиректа
  fetch/        HTTP-клиент с TTL-кэшем ([REMOTE], CURL)
  store/        ClickHouse (логи/постбэки/статистика) + Redis (счётчики/сессии)
  logbuf/       async-буфер событий → батч-вставка
  security/     argon2id, сессии, CSRF, constant-time
  server/       realip middleware (доверенные прокси)
  admin/        HTTP-хендлеры, файловые сторы, встроенный SPA (web/index.html)
migrations/clickhouse/   схема (применяется автоматически make infra-up)
deploy/docker-compose.yml ClickHouse + Redis для локальной разработки
configs/                 примеры и тестовые конфиги групп
docs/                    USAGE / ARCHITECTURE / SECURITY / STATUS (+ *.ru.md)
```

---

## Безопасность

argon2id-пароли · серверные сессии · CSRF на небезопасных методах · rate-limit
логина · доверенные прокси для XFF · параметризованные запросы ClickHouse ·
строгая валидация имён `.dat` (без path traversal) · только JSON на входе ·
секреты только через env. Полная модель: [`docs/SECURITY.ru.md`](docs/SECURITY.ru.md).

---

## Планы

В планах: cron-сервис (обновление IP-списков ботов, проверка доменов в
VirusTotal, мониторинг диска + Telegram-алерты), больше гео-фильтров (ASN/
организация/таймзона), Telegram-уведомления о конверсиях и др. См.
[`TODO.ru.md`](TODO.ru.md).

---

## Лицензия

[MIT](LICENSE).
