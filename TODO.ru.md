[English](TODO.md) · **Русский**

# TODO — KuzTDS

## Крупное
- [ ] **Блок 5 — cron-сервис** (отложено по решению):
  - обновление IP-списков ботов (`update_ip_url`, режимы replace/merge, парсинг секций `# se`)
  - проверка доменов в VirusTotal (`key_vt`, расписание, действия при заражении)
  - мониторинг свободного места на диске + уведомления в Telegram
  - очистка устаревших данных (в CH сейчас через TTL — пересмотреть при необходимости)

## Тесты — СДЕЛАНО (2026-06-07)
Покрытие добавлено по всем «дырам»; детали и таблица — в `docs/STATUS.md`.
- [x] cmd/engine — httptest-прогон конвейера (вынос в `engineDeps.root`)
- [x] internal/fetch — Get/GetCached/TTL/таймаут/ошибки
- [x] internal/admin — хендлеры (login/CSRF/группы/списки/ключи/пароль/экспорт) + file-сторы
- [x] internal/store — miniredis (sessions/LoginAllow/Rotate) + ClickHouse под `-tags=integration`
- [x] cmd/apiclient — round-trip с фейковым TDS (вынос в `newClientHandler`)

## Мелкое / опционально
- [ ] доп. тесты cmd/admin main()-обвязки (сейчас 0%; логика покрыта в internal/admin)
- [ ] captcha / TOTP (Google Authenticator) для логина админки
- [ ] drag-and-drop перестановка потоков мышью (сейчас ↑/↓ кнопками)
- [ ] apiset-UI для api-клиента (сейчас конфиг через env)
- [ ] per-stream Header/Comment в форме потока
- [ ] uniq_time в часах в UI (сейчас в секундах)

## Возможные фичи на будущее
- [ ] Гео-фильтры: ASN, организация (regex), таймзона UTC (`+3,+5:30`)
- [ ] Фильтр по GET-переменной (`get:str`)
- [ ] 4-я категория устройств «Другие» (Smart TV / TV Box)
- [ ] Тип редиректа `eval`
- [ ] Telegram-уведомления о конверсиях (макросы [PROFIT][GROUP][STREAM]...)
- [ ] Версии в фильтрах OS/браузера формата `windows:7;10`, `chrome:80;85` (сейчас «имя версия» подстрокой)
- [ ] Клонирование/миграция групп

## Сделано — см. docs/STATUS.md
