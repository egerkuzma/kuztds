[English](STATUS.md) · **Русский**

# STATUS — где мы и как продолжить

Снимок на 2026-08-20. Для деталей: `docs/USAGE.md`, `TODO.md`.

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
- **UI (переписан 2026-08-20)**: сайдбар-навигация слева, топбар справа (период,
  шестерёнка «Настройки», пользователь, выход), тёмная тема. «Группы» —
  редактор **мастер-детейл**: слева сворачиваемое дерево с поиском, справа
  панель, в которой открыта ровно одна форма — группы или потока. Обе панели
  скроллятся внутри себя, поэтому выбор потока не двигает страницу (в прежней
  вёрстке карточка потока стояла под формой группы, и её догоняли через
  `scrollIntoView`). Метка несохранённых изменений, подтверждение при уходе и
  закрытии, `Ctrl`/`Cmd`+`S`, обзорная таблица потоков с переходом в правку,
  ↑/↓ гасятся на краях. Покрыт тестами (`web_test.go` + `-tags=uitest` для JS),
  включая защиту от возврата реального вызова `.scrollIntoView(`.
- **Фикс**: фильтры country/lang/text работают и когда задан только `values`
  (без `raw`) — `router.go: cfgd()/orJoin()`.
- **Фикс (найдено e2e-тестами 2026-06-07):**
  1. Операторы WAP: `FlagB` теперь белый список конкретных операторов (раньше
     означало «любой оператор присутствует» — нельзя было таргетировать одного).
     `router.go` + комментарий в `config.go`.
  2. Кастомные `ip_list`-файлы потоков теперь подгружаются в `ipindex.Set`
     (`ipListFiles()` в `handler.go`, вызывается в `main.go`) — раньше per-stream
     фильтр по IP молча не срабатывал, если файл не из стандартного набора.

- **Фикс (ревью кода, 2026-08-20):**
  1. **Паника на подобранной cookie ротатора.** `ztrot_<group>_<stream>` пишет
     движок, но возвращает её посетитель. Отрицательное значение (`-5`) давало
     отрицательный индекс в списке вариантов `|||` и роняло запрос: проверялась
     только верхняя граница. Индексы из cookie и из Redis-счётчика `evenly`
     теперь проходят через `variantIndex()` (`main.go`), который на любом
     значении вне диапазона начинает цикл заново.
  2. **CSV-экспорт логов молча отдавал одну страницу.** `handleLogsExport`
     просил 50000 строк, а `CH.Logs` резал всё больше 1000 обратно до 100.
     Потолок стора теперь равен размеру экспорта (`maxLogRows`), а пагинация
     интерактивного эндпоинта переехала в админский слой (`maxPageRows`) —
     теперь оба лимита означают то, что написано (`clickhouse.go`, `admin.go`).
  3. **Счётчики лимитов могли превращаться в вечный бан.** `Firewall` ставил TTL
     только при положительном окне, поэтому включённый файрвол с `seconds: 0`
     оставлял бессмертный ключ в Redis и блокировал IP навсегда. То же самое —
     для лимита потока типа 2 без периода. Счётчики идут через `incrWithTTL()`,
     который гарантирует expiry и удаляет ключ, если `EXPIRE` не прошёл
     (`redis.go`).
  4. **`save_ip` дописывал дубли до следующего hot-reload.** Дедуп шёл по
     индексу в памяти, который обновляется раз в минуту, поэтому каждый хит с
     того же IP краулера добавлял ещё строку в `ip_<se>.dat`. Множество на
     уровне процесса (`savedBotIPs`) ограничивает это одной записью на IP
     (`bots.go`).
  5. Ключ `?api=` сравнивается через `security.EqualTokens`, а не `!=`
     (`api.go`) — так же, как все остальные секреты в проекте.
  6. `chance` разыгрывается через `hitPercent()`, а не повтором выражения
     `rand.Intn(100)+1` вручную — того самого, чей off-by-one уже однажды
     чинили в `api_mac` (`handler.go`).

  Регресс-тесты: `cmd/engine/rotator_test.go`, `cmd/engine/savebotip_test.go`,
  `internal/store/firewall_ttl_test.go`, `internal/store/loglimit_test.go`,
  `internal/admin/logs_limit_test.go`. Все пять падают на прежнем коде.

- **Изменение (2026-08-20):** группа теперь определяется по **первому сегменту
  пути**, а не по пути целиком — `/promo/iphone-15-sale.html` попадает в `promo`
  так же, как `/promo`. Сопоставление всего пути отправляло любую глубокую
  ссылку в trash-режим (по умолчанию пустой 200), хотя README и ARCHITECTURE всё
  это время обещали первый сегмент. `handler.go` + `routing_test.go`.

## Тесты (покрытие на 2026-08-20)
Прогон: `go test ./...` (юнит) и `go test -tags=integration ./...` (с CH+Redis).
`go vet ./...` — чисто. Команда покрытия: `go test -tags=integration ./... -cover`.

| Пакет | Покрытие | Заметка |
|-------|:--:|---|
| internal/fetch | 96.9% | httptest + подмена `now` для TTL |
| internal/logbuf | 93.6% | |
| internal/security | 84.3% | |
| internal/ipindex | 83.7% | |
| internal/geo | 82.6% | mmdb-тест |
| internal/router | 81.9% | + регресс country/lang values-only |
| internal/detect | 80.5% | |
| internal/render | 80.5% | |
| internal/config | 80.0% | |
| internal/store | 77.0% * | miniredis (Counters/sessions) + CH под `-tags=integration` |
| internal/admin | 74.7% | login/CSRF/группы/списки/ключи/пароль/экспорт + file-сторы + SPA (web_test.go) |
| internal/server | 73.2% | |
| cmd/apiclient | 71.6% | round-trip с фейковым TDS (`newClientHandler`) |
| cmd/engine | 66.3% | httptest-конвейер + хелперы + **e2e_test.go** (23 сквозных сценария: все типы редиректа, все макросы, боты, гео, фильтры, операторы, распределение, лимиты, фаервол, separation, расписание, chance, api-режим, матрица трафика) |
| cmd/admin | 0% | только `main()`-обвязка; логика в internal/admin |

\* `internal/store` на 2026-08-20 не перемерялся: ClickHouse не был поднят, и
тесты под `integration` скипаются (без них 32.9%). Значение 77.0% — последнее
измерение с поднятым ClickHouse.

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
