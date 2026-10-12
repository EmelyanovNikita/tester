# План по развитию

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
./tester -p config/config.json -s scripts/many_steps/call_rtp.txt       # один звонок с настоящим RTP
./tester -p config/config.json --continue-on-error -s scripts/simple   # все простые сценарии
./tester -p config/config.json -s scripts/mm_check                   # проверка кодеков и DTMF на MM
./tester -p config/config.json -s scripts/many_steps/call_rtp.txt --parallel 100 --rate 20   # 100 одновременных звонков
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
