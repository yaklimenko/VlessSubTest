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
| `store.go` | Накопление результатов: bbolt (`RunRecord`), выборки по диапазону дат |
| `scheduler.go` | Крон: probe каждые 4 часа (6 раз/сутки), конфиг `--cron-config` |

Поток данных: подписка (base64) → `FetchSubscription` → `ParseVlessURI` для каждой строки → для каждого ключа: выделить порт SOCKS5 (10800–10900) → старт движка (sing-box/xray) с конфигом в `/tmp/vlesssub/` → curl через `socks5h://127.0.0.1:<port>`.

---

## Накопление результатов (--log)

Демон может **накапливать** результаты прогонов в файле bbolt (встраиваемая KV-БД, файл вне контейнера). Включается флагом `--log`:

```bash
# локально
./vlesssubtest --port=8081 --log --db=/tmp/vlesssub/results.db

# в docker — файл монтируется volume'ом:
# command: --port=7070 --log --db=/data/results.db --cron-config=/data/config.json
# volumes:  ./data/vlesssubtest:/data
```

- Без `--log` ничего не пишется в БД (ручки `/runs` отвечают пустым списком).
- С `--log` сохраняются **все** прогоны: крон, ручные `POST /test` и `POST /probe`. Ошибки (подписка недоступна, пустая подписка) тоже сохраняются — с полем `error`.
- Каждый прогон — запись `RunRecord`: `id`, `kind` (`test`|`probe`), `subscription_url`, `started_at`/`finished_at`, `duration_sec`, счётчики `total/ok/degraded/failed`, `results[]` (per-key), при сбое — `error`. Ключ записи — UTC-время старта, поэтому выборки по диапазону дат работают по курсору без полного прохода.

## Крон-расписание (--cron-*)

В серверном режиме демон **сам** гоняет probe-тест по расписанию: каждые 4 часа (6 раз/сутки, сетка 00:00/04:00/08:00/12:00/16:00/20:00 UTC). Подписка по умолчанию — агрегатор панели:

```
https://example.com/sub/Example
```

Что тестировать — задаётся опциональным JSON-файлом `--cron-config` (перечитывается перед каждым запуском):

```json
{
  "subscriptions": [
    {"url": "https://example.com/sub/Example",
     "duration_sec": 180, "target_kbps": 4000, "parallel": 1,
     "probe_url": "https://203.0.113.5/speedtest/tcb.mp4"}
  ]
}
```

Дефолты можно переопределить флагами: `--cron-sub=URL` (заменяет весь список), `--cron-duration=N`, `--cron-target-kbps=N`, `--cron-parallel=N`, `--cron-probe-url=URL`. Файл отсутствует — тихо используются дефолты.

---

## HTTP API

Демон отвечает на `POST`-ручки тестов (`/test`, `/test-single`, `/probe`) и на `GET`-ручки истории (`/runs`, `/runs/{id}`). Все ответы — JSON.

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

### GET /runs — история прогонов с фильтром по диапазону дат

Панель забирает накопленные результаты `GET`-запросом (появляются в БД при запуске с `--log`).

```bash
curl 'http://localhost:7070/runs?from=2026-08-01&to=2026-08-06&detail=1&limit=100'
```

| Параметр | Описание | Дефолт |
|----------|----------|--------|
| `from` | Начало диапазона (включительно). `YYYY-MM-DD` (локальный день, с 00:00) или RFC3339-время | безгранично |
| `to` | Конец диапазона (включительно). `YYYY-MM-DD` (до конца дня) или RFC3339-время | безгранично |
| `limit` | Макс. записей | `100` (кламп сверху `1000`) |
| `detail` | `1` — вернуть per-key `results[]` каждого прогона; иначе только сводку | `0` |

Ошибки валидации (`from > to`, неверный формат времени) → `400`. Прогоны возвращаются **новейшими первыми**.

Ответ (без `detail=1` — `results` опускается):

```json
{
  "total": 2,
  "runs": [
    {"id": "2026-08-05T21:56:30.720873228Z", "kind": "probe",
     "subscription_url": "https://example.com/sub/Example",
     "started_at": "2026-08-06T00:56:30.720873228+03:00", "finished_at": "2026-08-06T00:56:50.954197074+03:00",
     "duration_sec": 20, "target_kbps": 500, "parallel": 1,
     "total": 2, "ok": 1, "degraded": 0, "failed": 1},
    {"id": "2026-08-05T21:56:29.228662040Z", "kind": "test",
     "subscription_url": "https://example.com/sub/Example",
     "started_at": "2026-08-06T00:56:29.22866204+03:00", "finished_at": "2026-08-06T00:56:30.705478603+03:00",
     "duration_sec": 1, "total": 2, "ok": 1, "degraded": 0, "failed": 1}
  ]
}
```

### GET /runs/{id} — один прогон

Полный прогон (включая `results[]`), где `{id}` — поле `id` из списка выше.

```bash
curl 'http://localhost:7070/runs/2026-08-05T21:56:30.720873228Z'
```

Ответ — один объект `RunRecord` (или `404`, если не найден). `GET /runs/{id}` при выключенном `--log` → `404`.

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
| `--log` | Накапливать результаты прогонов в bbolt | `false` |
| `--db=PATH` | Путь к файлу БД результатов | `/data/results.db` |
| `--cron-config=PATH` | JSON-конфиг крон-прогонов (опционально) | — |
| `--cron-sub=URL` | URL подписки для крона (переопределяет конфиг) | агрегатор панели |
| `--cron-duration=N` | Длительность probe на ключ для крона, сек | `180` |
| `--cron-target-kbps=N` | Целевая скорость probe для крона | `4000` |
| `--cron-parallel=N` | Параллельных ключей в кроне (1..2) | `1` |
| `--cron-probe-url=URL` | Тестовый файл для крона | `https://203.0.113.5/speedtest/tcb.mp4` |
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

Прод-контейнер: имя `vlesssubtest`, порт `7070`, сеть `vlesspanel-net`, restart `unless-stopped`, образ `vlesssubtest:latest`. Стек с панелью описан в `/home/klem/VlessPanelWebApp/docker-compose.yml` (docker compose v2). Сервис `vlesssubtest` монтирует хост-каталог `./data/vlesssubtest` в `/data` (файл БД `results.db` лежит **вне контейнера**) и запускается с `--log`:

```yaml
vlesssubtest:
  image: vlesssubtest:latest
  command: --port=7070 --log --db=/data/results.db --cron-config=/data/config.json
  volumes:
    - ./data/vlesssubtest:/data
```

**Пересоздание контейнера после пересборки образа:**

```bash
cd /home/klem/VlessSubTest && docker build -t vlesssubtest:latest .
cd /home/klem/VlessPanelWebApp && docker compose up -d vlesssubtest
```

⚠️ Опциональный крон-конфиг `/data/config.json` создаётся на **хосте** в `./data/vlesssubtest/` (см. раздел «Крон-расписание»). Если его нет — используются дефолты. Изменение файла подхватывается при следующем запуске крона.

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
| «Где docker compose?» | Стек панели переведён на **docker compose v2** (`/home/klem/VlessPanelWebApp/docker-compose.yml`); см. раздел «Прод» |
| `/probe` долго висит | Это нагрузочный тест (long-poll): `duration_sec` × число ключей. При разрыве клиента прогон прерывается через контекст запроса |
| `FetchSubscription` завис | У `FetchSubscription` нет таймаута (известное ограничение) — висящий URL подписки заблокирует обработчик. Известно, не критично |

---

## Известные ограничения / заметки

- `findSingBox()`/`findXray()` не делают полноценный поиск по PATH — проверяют каталог исполняемого файла и CWD. Бинари должны лежать рядом с бинарём.
- Пул портов 10800–10900 общий для `/test` и `/probe` (100 портов); несколько одновременных `/probe` с `parallel=2` могут его исчерпать. Для текущих нагрузок неактуально.
- `probe_url` не валидируется по схеме (endpoint внутренний; при публичном доступе — добавить allowlist).
- Крон-прогоны и ручные `/test`/`/probe` делят пул портов 10800–10900. Если крон стартовал с `parallel=2`, ручные запросы панели подождут свободного порта (`NO_PORT` — редко, для текущих нагрузок неактуально).
- `bbolt` v1.3.8 добавлен как зависимость (совместима с `golang:1.21` из Dockerfile); `go.sum` обязателен для `go mod download` при сборке образа.
- Онбординг для агентов: `AGENTS.md` в корне репозитория.

---

## Крон-расписание (системный cron)

Встроенный планировщик демона уже гоняет probe каждые 4 часа (6 раз/сутки). Если нужно управлять расписанием **извне** (системный cron) — например, держать демон «спящим», а прогоны запускать по crontab, — добавь в crontab хоста задачу, которая дёргает ручку `POST /probe`:

```cron
# 6 раз в сутки: в 00:00, 04:00, 08:00, 12:00, 16:00, 20:00
0 0,4,8,12,16,20 * * * curl -s -X POST http://localhost:7070/probe -H 'Content-Type: application/json' \
    -d '{"url":"https://example.com/sub/Example","duration_sec":180,"target_kbps":4000,"parallel":1}' >> /var/log/vlesssubtest-cron.log 2>&1
```

Эквивалентная запись через шаг: `0 */4 * * * curl ...` — тоже 6 раз в сутки.

**Как создать:**

```bash
crontab -e
# вставить строку выше, сохранить и выйти
crontab -l   # проверить
```

⚠️ Прогоны по внешнему cron попадут в БД только при `--log`, как и встроенные. При использовании системного cron встроенный планировщик демона можно «отключить», не передавая флаги `--cron-*`/`--cron-config` — но учти: встроенный крон запускается всегда, а разница лишь в том, кто инициирует прогон. Если нужен строго внешний крон — обсуждаемо (флаг отключения планировщика не реализован).
