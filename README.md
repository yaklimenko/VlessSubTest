# vlesssubtest

Go-демон для тестирования VLESS-ключей из VPN-подписок. Проверяет работоспособность ключей двумя способами:

- **Быстрый тест** (`/test`, `/test-single`) — проксирует трафик на `youtube.com` и `instagram.com` через sing-box, ключ считается OK, только если оба сервиса ответили 2xx/3xx.
- **Нагрузочный тест «думскролл»** (`/probe`) — долгий rate-limited тест с имитацией просмотра видео: для каждого ключа поднимается прокси, затем циклически качается тестовый файл с ограничением скорости до истечения `duration_sec`, и по накопленным метрикам выносится вердикт OK/DEGRADED/FAILED.

Два режима работы:

- **CLI**: `vlesssubtest url=<url подписки>` — протестировать и выйти.
- **HTTP-демон**: `vlesssubtest [--port=N]` — REST API для панелей (в проде запускается в docker, порт 7070).

Для xhttp-ключей используется **xray-core** (sing-box не умеет xhttp нативно), для всех остальных — **sing-box**.

---

## Архитектура (кратко)

| Файл | Назначение |
|------|------------|
| `main.go` | CLI-парсер, выбор режима (CLI/сервер), поиск бинарей sing-box/xray |
| `server.go` | HTTP-сервер: endpoint'ы `/test`, `/test-single`, `/probe` |
| `probe.go` | `/probe`: нагрузочный тест «думскролл», метрики, вердикты |
| `tester.go` | Движок: `startProxyEngine` (запуск/убийство sing-box или xray), пул SOCKS5-портов 10800–10900, curl-хелперы, `RunTests`/`TestOneKey` |
| `parser.go` | Парсинг `vless://` URI в `VlessKey` |
| `config.go` | Генерация JSON-конфига sing-box |
| `xrayconfig.go` | Генерация JSON-конфига xray (для xhttp-ключей) |
| `results.go` | Вывод результатов в CLI, загрузка подписки (base64) |

Поток данных: подписка (base64) → `FetchSubscription` → `ParseVlessURI` для каждой строки → для каждого ключа: выделить порт SOCKS5 (10800–10900) → старт движка (sing-box/xray) с конфигом в `/tmp/vlesssub/` → curl через `socks5h://127.0.0.1:<port>`.

---

## HTTP API

Демон отвечает только на `POST`. Все ответы — JSON.

### POST /test — быстрый тест всей подписки

```bash
curl -X POST http://localhost:7070/test \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com/sub/ExampleClient", "timeout": 10, "parallel": 0}'
```

| Поле | Описание | Дефолт |
|------|----------|--------|
| `url` | URL подписки (base64, обязательное) | — |
| `timeout` | Таймаут теста одного ключа, секунды | `10` |
| `parallel` | Макс. ключей одновременно; `0` = все | `0` (все) |

Ответ:

```json
{
  "total": 5,
  "ok": 3,
  "results": [
    {"key_idx": 1, "ip": "203.0.113.4", "remark": "perMonth-ExampleClient",
     "status": "OK", "youtube": "OK", "instagram": "OK"},
    {"key_idx": 2, "ip": "10.0.0.1", "remark": "key2",
     "status": "FAILED", "reason": "youtube: TIMEOUT",
     "youtube": "FAILED (TIMEOUT)", "instagram": "OK"}
  ]
}
```

Ключ **OK** только если **оба** сервиса (youtube + instagram) вернули 2xx/3xx. Причины отказа: `TIMEOUT`, `CONNECTION_FAILED`, `HTTP_<code>`, `CURL_EXIT_<code>`, `SING_BOX_START_FAILED`, `XRAY_NOT_FOUND`, `NO_PORT`, `parse_error`.

### POST /test-single — быстрый тест одного ключа

```bash
curl -X POST http://localhost:7070/test-single \
  -H 'Content-Type: application/json' \
  -d '{"vless": "vless://uuid@host:port?type=tcp&security=reality#remark", "timeout": 10}'
```

| Поле | Описание | Дефолт |
|------|----------|--------|
| `vless` | Полная vless:// ссылка (обязательное) | — |
| `timeout` | Таймаут, секунды | `10` |

Ответ — один объект результата (как элемент `results[]` из `/test`, `key_idx: 0`).

### POST /probe — длительный нагрузочный тест «думскролл»

```bash
curl -X POST http://localhost:7070/probe \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com/sub/ExampleClient", "duration_sec": 60, "target_kbps": 4000, "parallel": 1}'
```

| Поле | Описание | Дефолт |
|------|----------|--------|
| `url` | URL подписки (обязательное) | — |
| `probe_url` | Тестовый файл для скачивания | `https://203.0.113.5/speedtest/tcb.mp4` (приватный ресурс: nginx + Let's Encrypt на Стокгольм-VPS) |
| `duration_sec` | Длительность прогона на ключ, сек | `180` (кламп сверху `900` = 15 мин) |
| `target_kbps` | Целевая скорость сессии | `4000` |
| `parallel` | Ключей одновременно (внутри ключа — строго последовательно) | `1` (кламп `1..2`) |

⚠️ **Единицы измерения — килобиты в секунду (Кбит/с), не килобайты.** Двоичные килобиты: 1 Кбит = 1024 бита = 128 байт. `target_kbps=4000` = кап `--limit-rate 512000` (bytes) в curl = 500 КБ/с. `avg_speed_kbps` считается в тех же единицах (`bytes/s / 128`), поэтому рабочий ключ при `target=4000` показывает **~4000 Кбит/с**.

**Как работает:**

1. Подписка тянется и парсится тем же кодом, что `/test`. Невалидные строки → `FAILED (parse_error)`.
2. Для каждого ключа (до `parallel` одновременно):
   - выделяется порт SOCKS5 (10800–10900), стартует движок — sing-box, или **xray для xhttp-ключей** (без бинаря xray → `FAILED (XRAY_NOT_FOUND)`);
   - **connectivity check**: curl на `probe_url` без rate-limit, кап 10 с. Проход = HTTP 2xx/3xx и скачано > 0 байт (медленный, но живой ключ не отсеивается). Непроход → `FAILED` (`CONNECTION_FAILED` / `TIMEOUT` / `HTTP_<code>` / `CURL_EXIT_<code>`);
   - **цикл «думскролл»** до истечения `duration_sec`: сессия = отдельный curl `--limit-rate <target_kbps*128> --max-time 10` (чанк ~5 МБ при целевом rate), затем пауза 2–5 с (случайная). Exit code 28 (max-time) — штатный конец чанка; другие ненулевые коды или не-2xx/3xx → `reconnects++`;
   - из `-w` собираются `%{speed_download} %{time_starttransfer} %{http_code} %{size_download}`.
3. При разрыве клиента прогон прерывается (контекст запроса пробрасывается в движок и цикл) — ресурсы не сжигаются.

**Метрики ключа:** `avg_speed_kbps` (среднее по всем сессиям, Кбит/с; упавшие сессии дают 0 в среднее и знаменатель), `stability_pct` (доля сессий со скоростью ≥ 0.8·target, знаменатель — все сессии), `reconnects`, `latency_ms` (средний `time_starttransfer`), `total_downloaded_mb`, `sessions_ok`, `sessions_fail`, `duration_sec` (фактическое время).

**Вердикты** (константы в `probe.go`):

- `OK` — `avg_speed >= 0.8*target` **И** `stability >= 80%`;
- `DEGRADED` — `avg_speed >= 0.5*target` **ИЛИ** `stability >= 50%`;
- иначе `FAILED` (reason `VERDICT_FAILED`, или `ALL_SESSIONS_FAILED`, если упали все сессии).

Ответ:

```json
{
  "total": 6, "ok": 6, "degraded": 0, "failed": 0,
  "results": [
    {"key_idx": 0, "remark": "perMonth-ExampleClient", "ip": "203.0.113.5",
     "status": "OK", "avg_speed_kbps": 4000.8, "stability_pct": 100,
     "reconnects": 0, "latency_ms": 633.0, "total_downloaded_mb": 14.7,
     "sessions_ok": 3, "sessions_fail": 0, "duration_sec": 40}
  ]
}
```

---

## CLI-режим

```bash
./vlesssubtest url=<url подписки> [options]
```

| Параметр | Описание | Дефолт |
|----------|----------|--------|
| `url=<url>` | URL подписки (обязателен в CLI-режиме) | — |
| `--timeout=N` | Таймаут теста ключа, сек | `10` |
| `--parallel=N` | Макс. параллельных ключей | все |
| `--verbose` | Показывать stderr sing-box/xray при ошибках | `false` |
| `--keep-logs` | Не удалять временные файлы в `/tmp/vlesssub` | `false` |
| `--port=N` | Порт HTTP-сервера | `8080` |
| `--help`, `-h` | Справка | — |

```bash
./vlesssubtest url=https://example.com/sub/ExampleClient
./vlesssubtest url=https://example.com/sub --timeout=15 --parallel=5
```

Вывод: `vlesssubtest results: N/M OK` + по строке на ключ (`youtube: OK; instagram: FAILED (HTTP_403)` и т.п.).

---

## Сборка

### Локальная (Go)

```bash
export PATH=$PATH:/usr/local/go/bin
go build -o vlesssubtest .
go vet .
```

Бинари **sing-box** и **xray** должны лежать рядом с исполняемым файлом (не в PATH — поиск работает по каталогу исполняемого файла и CWD). В репозитории они уже есть; если нет:

```bash
docker cp vlesssubtest:/usr/local/bin/sing-box .
docker cp vlesssubtest:/usr/local/bin/xray .
```

### Docker-образ

```bash
cd /home/klem/VlessSubTest
docker build -t vlesssubtest:latest .
```

Dockerfile: `golang:1.21` builder → `debian:bookworm-slim` runtime; sing-box 1.13.13 и xray v26.3.27 скачиваются при сборке; в образе есть `curl`. ⚠️ `EXPOSE 8080` в Dockerfile — косметика, реальный порт задаётся `--port=` и `-p`.

---

## Запуск

### Локальный

```bash
./vlesssubtest --port=8081
```

⚠️ Порт `8080` на хосте занят filebrowser'ом — для локальных прогонов используй `8081` и выше.

### Прод (docker)

Прод-контейнер: имя `vlesssubtest`, порт `7070`, сеть `vlesspanel-net`, restart `unless-stopped`, образ `vlesssubtest:latest`.

**Пересоздание контейнера после пересборки образа:**

```bash
docker stop vlesssubtest && docker rm vlesssubtest
docker run -d --name vlesssubtest \
  -p 7070:7070 \
  --network vlesspanel-net \
  --restart unless-stopped \
  vlesssubtest:latest --port=7070
```

⚠️ **docker compose здесь не используется** (плагина нет на хосте) — только `docker run`. В README проекта VlessPanelWebApp команда указана без `--restart`; вариант с `--restart unless-stopped` лучше — контейнер переживает перезагрузку хоста.

### Интеграция с VlessPanelWebApp

Панель (отдельный проект `/home/klem/VlessPanelWebApp`) ходит на демон по имени контейнера: `http://vlesssubtest:7070`. Статус демона в панели: `http://localhost:9090/api/vlesssubtest-status`. Прод-панели 3X-UI управляются **только** через API панели (`http://localhost:9090/api/panels`) — напрямую их не трогать.

---

## Логи и временные файлы

- Логи демона — **stderr** (в проде: `docker logs vlesssubtest`).
- Конфиги движка: `/tmp/vlesssub/test-{port}.json` (sing-box), `/tmp/vlesssub/xray-test-{port}.json` (xray); данные sing-box: `/tmp/vlesssub/data-{port}/`; лог sing-box: `/tmp/vlesssub/sing-box-{port}.log`.
- Всё удаляется автоматически по завершении теста ключа (если не задан `--keep-logs`).
- `--verbose` пробрасывает stderr движка в stderr демона.

---

## Troubleshooting

| Симптом | Причина → решение |
|---------|-------------------|
| `Error: sing-box binary not found. Place it next to vlesssubtest or in PATH.` | Бинаря нет рядом с исполняемым файлом → `docker cp vlesssubtest:/usr/local/bin/sing-box .` |
| `FAILED (XRAY_NOT_FOUND)` на xhttp-ключе | Тестируется xhttp-ключ без бинаря xray → `docker cp vlesssubtest:/usr/local/bin/xray .` (xray нужен только для xhttp) |
| `SING_BOX_START_FAILED: port not ready` | Движок не поднялся → запусти с `--verbose`/`--keep-logs`, посмотри `/tmp/vlesssub/sing-box-{port}.log` |
| Порт занят при локальном запуске | `8080` на хосте занят filebrowser'ом → запускай на `8081+` |
| В `/probe` скорость ~`target` — «всё сломалось»? | Нет, это норма: единицы — **Кбит/с**, `target=4000` = `--limit-rate 500K` = 500 КБ/с (4000 Кбит/с). Это сознательное решение |
| «Где docker compose?» | Не используется — только `docker run` (см. раздел «Прод») |
| `/probe` долго висит | Это нагрузочный тест (long-poll): `duration_sec` × число ключей. При разрыве клиента прогон прерывается через контекст запроса |
| `FetchSubscription` завис | У `FetchSubscription` нет таймаута (известное ограничение) — висящий URL подписки заблокирует обработчик. Известно, не критично |

---

## Известные ограничения / заметки

- `findSingBox()`/`findXray()` не делают полноценный поиск по PATH — проверяют каталог исполняемого файла и CWD. Бинари должны лежать рядом с бинарём.
- Пул портов 10800–10900 общий для `/test` и `/probe` (100 портов); несколько одновременных `/probe` с `parallel=2` могут его исчерпать. Для текущих нагрузок неактуально.
- `probe_url` не валидируется по схеме (endpoint внутренний; при публичном доступе — добавить allowlist).
- Онбординг для агентов: `AGENTS.md` в корне репозитория.
