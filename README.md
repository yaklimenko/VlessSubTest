# Vless Subscription Test Utility — Spec

**Кодовое имя:** `vlesssubtest`  
**Назначение:** CLI-утилита для пакетной проверки работоспособности всех ключей из vless-подписки (3X-UI панель).  
**Целевая платформа:** Linux x86_64 (желательно и arm64).  
**Зависимости:** `sing-box` (статический бинарник), `curl`.

---

## 1. Общая схема работы

```
vlesssubtest url=https://example.com/sub/ExampleClient
```

1. **GET** $URL → base64-строка со списком vless:// ссылок
2. Декодировать base64 → массив vless:// URI
3. Каждый URI конвертировать в sing-box JSON-конфиг
4. Запустить тесты: для каждого ключа (или параллельно) поднять sing-box с этим outbound'ом, через socks5 сходить на youtube.com, проверить ответ
5. Вывести таблицу результатов

---

## 2. Формат входных данных

Подписка 3X-UI панели → **base64** → внутри одна vless:// ссылка на строку.

Пример:

```
vless://c21...@203.0.113.5:443?&security=reality&pbk=...&fp=chrome&sid=...&spx=%2F&type=tcp&flow=xtls-rprx-vision
vless://c21...@203.0.113.4:443?&security=reality&pbk=...&fp=chrome&sid=...&spx=%2F&type=tcp&flow=xtls-rprx-vision
```

Поле `#` / `remark` в ссылке может содержать имя клиента или inbound — выводить как есть.

Каждый ключ имеет **индекс (keyIdx)** — порядковый номер в подписке, начиная с 0. Индекс выводится для каждого результата и помогает ориентироваться при ручной отладке/исправлении.

---

## 3. Конвертация vless:// → sing-box конфиг

Использовать встроенную команду sing-box (поставляется вместе с утилитой):

```bash
sing-box tools convert -f v2ray:// <vless_uri>
```

На выходе → JSON-конфигурация outbound для sing-box.  
Если команда не принимает одиночные URI — можно скармливать временным файлом.

**Fallback:** если `tools convert` не срабатывает — парсить URI вручную (должен уметь заполнять все поля: address, port, uuid, flow, packet_encoding, tls — reality — public_key, short_id, server_name, fingerprint).

---

## 4. Тестирование одного ключа

### 4.1. Генерация конфига

Для каждого outbound генерируется минимальный sing-box конфиг:

```json
{
  "log": { "level": "error", "output": "/tmp/vlesssub/sing-box-{port}.log" },
  "inbounds": [
    {
      "type": "socks",
      "tag": "socks-in",
      "listen": "127.0.0.1",
      "listen_port": {random_port}
    }
  ],
  "outbounds": [
    { outbound из vless:// ссылки },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "rules": [
      { "outbound": "socks-in" }
    ],
    "auto_detect_interface": true
  }
}
```

**Важно:** конфиг НЕ содержит TUN. Трафик идёт только через явное подключение к socks5. Системные соединения не трогаются.

### 4.2. Запуск

```bash
sing-box run -c /tmp/vlesssub/test-{port}.json -D /tmp/vlesssub/data-{port} &
PID=$!
```

### 4.3. Проверка

```bash
curl -x socks5://127.0.0.1:{port} \
  -sS -o /dev/null -w '%{http_code}' \
  --connect-timeout 5 --max-time 10 \
  https://youtube.com
```

- **HTTP 200+** и тело непустое → OK
- Всё остальное (ошибка, таймаут, HTTP 4xx/5xx, пустое тело, DNS fail) → FAILED
- Если curl вернул ненулевой exit code → FAILED

### 4.4. Очистка

```bash
kill $PID
rm -rf /tmp/vlesssub/test-{port}.json /tmp/vlesssub/data-{port}
```

---

## 5. Параллельное тестирование

- Каждый ключ получает **свой socks5 порт** (диапазон 10800–10900, проверка занятости).
- Все sing-box инстансы стартуют **одновременно**.
- Все curl-тесты запускаются **параллельно** (xargs -P или parallel, или асинхронные сабпроцессы).
- Ждать завершения всех.
- Собрать результаты, вывести.
- Убить все sing-box инстансы одной командой: `pkill -f "sing-box -c /tmp/vlesssub"` или сохранять PIDs.

---

## 6. Вывод результатов

Каждая строка начинается с индекса ключа в подписке (с нуля):

### Успешный тест
```
keyIdx: {N} | {IP} | {remark} | OK
```

### Проваленный тест (ключ распарсился, но не работает)
```
keyIdx: {N} | {IP} | {remark} | FAILED, причина `{REASON}`
```

### Ключ не удалось распарсить
```
keyIdx: {N} | FAILED to parse
```

**Примеры:**

```
keyIdx: 0 | FAILED to parse
keyIdx: 1 | 203.0.113.5 | permonth-ExampleClient | OK
keyIdx: 2 | 203.0.113.4 | Anton-PocoX6 | FAILED, причина `CONNECTION_FAILED`
keyIdx: 3 | example.com | Sergey-Nas | OK
```

**Разделитель:** ` | ` (пробел-пайп-пробел).  
**IP-адрес** — взять из vless:// URI (hostname). Если там домен, а не IP — выводить как есть.  
**OK / FAILED** — заглавными.  
**REASON** — короткий код причины (см. раздел 7), в обратных кавычках.  
**Порядок:** по возрастанию keyIdx (как в подписке).

Перед таблицей — короткий заголовок (например, `vlesssubtest results:`), и сводка: сколько OK из скольких всего ключей.

---

## 7. Обработка ошибок

| Ситуация | Вывод | REASON |
|---|---|---|
| Подписка не ответила (404/таймаут) | `Error: subscription unreachable` → exit 1 | — |
| В подписке 0 ключей | `Error: empty subscription` → exit 1 | — |
| i-й ключ не парсится | `keyIdx: {i} \| FAILED to parse` | — |
| sing-box не стартанул (bad config) | FAILED | `SING_BOX_START_FAILED` |
| Curl не смог подключиться | FAILED | `CONNECTION_FAILED` |
| Curl получил не-200 | FAILED | `HTTP_{код}` |
| Таймаут (10s по умолчанию) | FAILED | `TIMEOUT` |
| curl вернул ненулевой exit code | FAILED | `CURL_EXIT_{код}` |

---

## 8. Аргументы командной строки

```
vlesssubtest url=<subscription_url> [options]
```

Опции (опционально):

| Флаг | Описание | Default |
|---|---|---|
| `url=` | URL подписки | обязательный |
| `--timeout` | Таймаут на один тест, секунд | 10 |
| `--parallel` | Максимум параллельных тестов | все сразу |
| `--verbose` | Выводить логи sing-box при ошибках | off |
| `--keep-logs` | Не чистить /tmp/vlesssub | off |

---

## 9. Комплект поставки

```
vlesssubtest/
├── vlesssubtest          # Исполняемый файл (скрипт или скомпилированный бинарник)
└── sing-box              # Статический бинарник sing-box (Linux amd64)
```

**Реализация: Go** — один self-contained бинарник. Вызов sing-box через `os/exec`. Никаких внешних зависимостей, кроме самого sing-box рядом.

---

## 10. sing-box: где взять

- Release: https://github.com/SagerNet/sing-box/releases
- Брать `sing-box-{version}-linux-amd64.tar.gz`
- Внутри один бинарник — ничего ставить не надо, просто положить рядом со скриптом

**Вариант для `tools convert`:**

```bash
echo "vless://..." | base64 -d 2>/dev/null || cat
# или sing-box tool convert -f v2ray:// uri
```

Уточнить поддержку `tools convert` в target-версии. Если недоступна — написать свой парсер vless:// URI (формат стандартизирован, все поля в query-параметрах).

---

## 11. Пример полного прогона

```
$ vlesssubtest url=https://example.com/sub/ExampleClient

vlesssubtest results: 3/5 OK

keyIdx: 0 | FAILED to parse
keyIdx: 1 | 203.0.113.5 | permonth-ExampleClient | OK
keyIdx: 2 | 203.0.113.4 | Anton-PocoX6 | OK
keyIdx: 3 | 203.0.113.4 | Anton-iPhone | FAILED, причина `TIMEOUT`
keyIdx: 4 | example.com | Sergey-Nas | OK
```

---

## 12. Заметки для кодера

- Sing-box умеет работать с конфигами где **нет TUN** — не перехватывает трафик системы. Проверено.
- Важно не забыть `auto_detect_interface: true` в конфиге, иначе может не быть роутинга.
- Для `tools convert` из v2ray-строки можно сделать временный файл и передать.
- REALITY-поля (publicKey, shortId, serverName, fingerprint) — обязательны для reality-ключей.
- Если vless:// ссылка содержит flow=xtls-rprx-vision — убедиться, что sing-box его поддерживает (да, поддерживает).
- Имена inbound'ов для тегов использовать без спецсимволов (только латиница, дефис, подчёркивание).
