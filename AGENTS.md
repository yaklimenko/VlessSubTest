# AGENTS.md — онбординг для агентов

**Что это:** Go-демон тестирования VLESS-ключей из VPN-подписок: быстрый тест (`/test`, `/test-single` — youtube+instagram через sing-box) и длительный нагрузочный тест «думскролл» (`/probe` — rate-limited скачивание файла с метриками и вердиктами OK/DEGRADED/FAILED). Для xhttp-ключей движок — xray, для остальных — sing-box.

## Файлы (1 строка на файл)

- `main.go` — CLI-парсер, режим CLI/сервер, поиск бинарей sing-box/xray
- `server.go` — HTTP-сервер: `/test`, `/test-single`, `/probe`
- `probe.go` — `/probe`: цикл «думскролл», метрики, вердикты
- `tester.go` — движок: `startProxyEngine` (sing-box/xray), пул портов SOCKS5 10800–10900, curl-хелперы
- `parser.go` — парсинг `vless://`
- `config.go` / `xrayconfig.go` — генерация конфигов sing-box / xray
- `results.go` — вывод CLI, загрузка подписки (base64)
- `PROBE_REPORT.md` — история реализации `/probe` (частично устарел: единицы/дефолт probe_url см. в `probe.go`)

## Команды (copy-paste)

```bash
# сборка (локально)
export PATH=$PATH:/usr/local/go/bin && go build -o vlesssubtest . && go vet .

# запуск локально (8080 занят filebrowser'ом — только 8081+!)
./vlesssubtest --port=8081

# быстрый тест
curl -X POST http://localhost:8081/test -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/sub/ExampleClient"}'
# нагрузочный тест (пример из прода)
curl -X POST http://localhost:7070/probe -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/sub/ExampleClient","duration_sec":60,"target_kbps":4000,"parallel":1}'

# docker-образ
cd /home/klem/VlessSubTest && docker build -t vlesssubtest:latest .

# пересоздать прод-контейнер (ТОЛЬКО по явному запросу владельца)
docker stop vlesssubtest && docker rm vlesssubtest
docker run -d --name vlesssubtest -p 7070:7070 --network vlesspanel-net \
  --restart unless-stopped vlesssubtest:latest --port=7070
```

## Логи

- Демон логирует в **stderr**: прод — `docker logs vlesssubtest`, локально — вывод терминала.
- Конфиги/логи движка: `/tmp/vlesssub/` (удаляются после теста; `--keep-logs`/`--verbose` для отладки).

## Ключевые правила

- **Единицы в `/probe` — килобиты/с (Кбит/с), НЕ килобайты.** Двоичные килобиты (1 Кбит = 1024 бита = 128 байт): `target_kbps=4000` = кап `--limit-rate 512000` (curl считает байты) = 500 КБ/с; `avg_speed_kbps` в тех же единицах (`bytes/s / 128`). Сознательное решение владельца — не менять.
- **Не трогать прод без явного запроса:** не пересобирать образ, не пересоздавать контейнер `vlesssubtest`, не коммитить в git. Документация/код — пожалуйста, деплой — только по команде.
- **docker compose НЕ используется** (плагина нет на хосте) — только `docker run`.
- Прод-панели 3X-UI управляются только через VlessPanelWebApp (`http://localhost:9090/api/panels`) — напрямую не трогать.
- Локально порт `8080` занят filebrowser'ом — тестировать на `8081+`.
- Бинари `sing-box`/`xray` должны лежать рядом с бинарём vlesssubtest (не в PATH). В репо они есть; если нет — `docker cp vlesssubtest:/usr/local/bin/sing-box .` (и `xray`).
- Go 1.21 (go.mod); локальный toolchain: `/usr/local/go/bin/go`. Подробности API/деплоя: `README.md`.
