# vlesssubtest

Утилита для тестирования VLESS-ключей через проксирование трафика на `youtube.com` и `instagram.com`. Два режима: CLI и HTTP-демон.

## Принцип работы

1. Загружает подписку (base64) по указанному URL или принимает одиночную `vless://` ссылку.
2. Парсит `vless://` URI.
3. Для каждого ключа запускает два экземпляра `sing-box` (отдельная SOCKS5-прокси для youtube.com и для instagram.com).
4. Выполняет `curl` через каждый прокси к соответствующему целевому ресурсу.
5. Выводит результат: для каждого ключа — статус по youtube.com и instagram.com.

## Docker

Сборка образа (sing-box загружается с GitHub автоматически):

```bash
docker build -t vlesssubtest .
```

CLI-режим:

```bash
docker run --rm vlesssubtest url=https://example.com/sub
```

Режим демона:

```bash
docker run --rm -p 8080:8080 vlesssubtest --port=8080
```

## HTTP API

Если `vlesssubtest` запущен без аргумента `url=`, он стартует как HTTP-сервер.

### POST /test — тест всей подписки

```bash
curl -X POST http://localhost:8080/test \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com/sub", "timeout": 10, "parallel": 0}'
```

Ответ:

```json
{
  "total": 5,
  "ok": 3,
  "results": [
    {
      "key_idx": 1,
      "ip": "203.0.113.4",
      "remark": "perMonth-ExampleClient",
      "status": "OK",
      "youtube": "OK",
      "instagram": "OK"
    }
  ]
}
```

### POST /test-single — тест одной vless-ссылки

```bash
curl -X POST http://localhost:8080/test-single \
  -H 'Content-Type: application/json' \
  -d '{"vless": "vless://uuid@host:port?...", "timeout": 10}'
```

Ответ:

```json
{
  "key_idx": 0,
  "ip": "46.21.83.114",
  "remark": "FriendsFamily-Anton",
  "status": "OK",
  "youtube": "OK",
  "instagram": "OK"
}
```

## Локальная сборка

```bash
go build -o vlesssubtest .
```

Бинарный файл `sing-box` должен находиться рядом с `vlesssubtest` или в `PATH`.

## Использование (CLI)

```
vlesssubtest url=<subscription_url> [options]
```

### Параметры

| Параметр          | Описание                                      | По умолчанию |
|-------------------|-----------------------------------------------|--------------|
| `url=<url>`       | URL подписки (обязательно в CLI-режиме)       | —            |
| `--timeout=N`     | Таймаут теста в секундах (curl + sing-box)    | `10`         |
| `--parallel=N`    | Максимальное число параллельно тестируемых ключей | `all`    |
| `--port=N`        | Порт HTTP-сервера в режиме демона             | `8080`       |
| `--verbose`       | Показывать логи sing-box при ошибках          | `false`      |
| `--keep-logs`     | Не удалять временные файлы в `/tmp/vlesssub`  | `false`      |
| `--help`, `-h`    | Показать справку                              | —            |

### Пример

```bash
vlesssubtest url=https://example.com/sub/ExampleClient
vlesssubtest url=https://example.com/sub --timeout=15 --parallel=5
```

## Формат вывода (CLI)

```
vlesssubtest results: 3/5 OK

keyIdx: 1 | 203.0.113.4  | perMonth-ExampleClient | youtube: OK; instagram: OK
keyIdx: 2 | 10.0.0.1       | key2              | youtube: FAILED (TIMEOUT); instagram: OK
keyIdx: 3 | 192.168.1.1    | key3              | youtube: OK; instagram: FAILED (HTTP_403)
keyIdx: 4 | FAILED to parse
keyIdx: 5 | 172.16.0.1     | key5              | youtube: FAILED (CONNECTION_FAILED); instagram: FAILED (TIMEOUT)
```

Ключ считается **OK**, только если **оба** сервиса (`youtube.com` и `instagram.com`) ответили успешно (HTTP 2xx/3xx).

## Временные файлы

- Конфиги: `/tmp/vlesssub/test-{port}.json`
- Данные sing-box: `/tmp/vlesssub/data-{port}/`
- Логи: `/tmp/vlesssub/sing-box-{port}.log`

Удаляются автоматически после теста, если не указан флаг `--keep-logs`.
