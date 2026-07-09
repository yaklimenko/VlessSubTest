# vlesssubtest

Утилита для тестирования VLESS-ключей из подписки через проксирование трафика на `youtube.com` и `instagram.com`.

## Принцип работы

1. Загружает подписку (base64) по указанному URL.
2. Парсит `vless://` URI из подписки.
3. Для каждого ключа запускает два экземпляра `sing-box` (отдельная SOCKS5-прокси для youtube.com и для instagram.com).
4. Выполняет `curl` через каждый прокси к соответствующему целевому ресурсу.
5. Выводит результат: для каждого ключа — статус по youtube.com и instagram.com.

## Требования

- Бинарный файл `sing-box` должен находиться рядом с `vlesssubtest` или в `PATH`.
- Доступные локальные порты в диапазоне `10800-10900` (по 2 порта на ключ).

## Сборка

```bash
go build -o vlesssubtest .
```

## Использование

```
vlesssubtest url=<subscription_url> [options]
```

### Параметры

| Параметр          | Описание                                      | По умолчанию |
|-------------------|-----------------------------------------------|--------------|
| `url=<url>`       | URL подписки (обязательно)                    | —            |
| `--timeout=N`     | Таймаут теста в секундах (curl + sing-box)    | `10`         |
| `--parallel=N`    | Максимальное число параллельно тестируемых ключей | `all`    |
| `--verbose`       | Показывать логи sing-box при ошибках          | `false`      |
| `--keep-logs`     | Не удалять временные файлы в `/tmp/vlesssub`  | `false`      |
| `--help`, `-h`    | Показать справку                              | —            |

### Пример

```bash
vlesssubtest url=https://example.com/sub/ExampleClient
vlesssubtest url=https://example.com/sub --timeout=15 --parallel=5
```

## Формат вывода

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
