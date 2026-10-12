# План по развитию

## Ораганизовать работу прям с скриптами
Добавить:
- for (сделано)
- sleep (сделано)
- log (сделано)
- if (сделано, базовые сравнения; `&&`/`||` и `else if` пока нет)

## Нагрузочное тестирование
Сделано (`--parallel`, `--calls`, `--rate`, переменная `INSTANCE`, сводка; см. «Нагрузочный режим»).

## Сделать работу с несколькими  тестерами, как несколько управляющих серверов
Ну это сильно в будущем

## Добавить RTP трафик
Аудио сделано (PCMU, PCMA, G.722, L16, DTMF; отправка и приём, запись в WAV, см. «RTP-трафик»). Видео и остальные кодеки — позже.

## Работа
- Изменить работу со сложными переменными, надо чтобы сложные переменные создавались просто через {"field1" : "string_value1", "field2" : vaiable, "field3" : 8, "field4" : ENUM_4}
- Добавить в конфигу поле задержка перед стартом (сделано: `start_delay_sec`, см. «Подключение к MM»)
- Передача сценариев через аргумент или конфиг, запуск папки по очереди (сделано, см. «Несколько сценариев»)

## Документация

| Документ | Для кого | Что внутри |
|---|---|---|
| [docs/USAGE.md](docs/USAGE.md) | тот, кто запускает тесты | сборка, флаги, конфиг, подключение к MM, нагрузочный режим, проверка кодеков, как читать лог и счётчики RTP, диагностика |
| [docs/SCRIPTING.md](docs/SCRIPTING.md) | тот, кто пишет сценарии | справочник языка: значения и переменные, вызовы MM, `for`/`if`/`sleep`/`log`/`fail`/`connect`, `wait_event`, RTP-команды, примеры |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | разработчик тестера | пакеты, потоки данных, как добавить команду, кодек, поле статистики, тесты, ограничения |

## Быстрый старт

```
go build -o tester ./cmd/tester
./tester -p config/config.json -s scripts/simple/channel_add.txt        # один сценарий
./tester -p config/config.json -s scripts/many_steps/call_rtp.txt       # звонок с настоящим RTP
./scripts/mm_check/run.sh config/config.json                            # проверка кодеков и DTMF на MM
./scripts/many_steps/load_100_calls.sh config/config.json               # 100 одновременных звонков
go test ./...                                                           # тесты тестера (MM не нужен)
```

Минимальный сценарий:

```
connect()
rtp = rtp_open()
resp = ChannelAdd(SESSION_ID, 0, 0, true, rtp.sdp, 0, "")
log("канал:", resp.channelId)
resp = ChannelRemove(SESSION_ID, resp.channelId)
```
