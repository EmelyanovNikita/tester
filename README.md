# План по развитию

## Документация

| Документ | Для кого | Что внутри |
|---|---|---|
| [docs/USAGE.md](docs/USAGE.md) | тот, кто запускает тесты | сборка, флаги, конфиг, подключение к MM, нагрузочный режим, проверка кодеков, как читать лог и счётчики RTP, диагностика |
| [docs/SCRIPTING.md](docs/SCRIPTING.md) | тот, кто пишет сценарии | справочник языка: значения и переменные, вызовы MM, `for`/`if`/`sleep`/`log`/`fail`/`connect`, `wait_event`, RTP-команды, примеры |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | разработчик тестера | пакеты, потоки данных, как добавить команду, кодек, поле статистики, тесты, ограничения |

## Быстрый старт

Инструкция для Linux (Ubuntu/Debian). Предполагается, что вы уже сделали `git clone` и ничего больше не установлено (можно делать по инструкции, а можно просто использовать скрипт `setup.sh`).

### Шаг 1. Установить Go

Нужен Go версии 1.22 или новее:

```
wget https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

### Шаг 2. Собрать тестер

Перейдите в папку тестера (куда сделали `git clone`) и выполните:

```
cd tester
go build -o tester ./cmd/tester
```

В первый раз сборка скачивает библиотеки и идёт минуту-две. Успех — команда ничего не печатает, а в папке появился файл `tester`.

### Шаг 3. Настроить конфиг

Откройте `config/config.json` и поправьте три места:

```json
{
	"proto_root": "/home/user/ecss-media-manager-api-proto",
	"proto_files": [
		"mm/client_api/v1/mm_client_api.proto",
		"mm/objects/v1/mm_objects.proto",
		"mm/server_api/v1/mm_server_api.proto"
	],
	"local_server": {
		"address": "0.0.0.0",
		"port": 7000
	},
	"rtp": {
		"ip": "10.30.100.108"
	}
}
```

- `proto_root` — полный путь к папке с proto
- `rtp.ip` — ваш адрес.
- `local_server.port` — порт, на котором тестер ждёт MM; обычно оставляют `7000`.

### Шаг 4. Проверить, что тестер собрался правильно

```
go test ./...
```

Все строки должны быть `ok` (или `no test files`). MM для этого не нужен.

### Шаг 5. Указать MM адрес тестера

В настройках MM указать адрес тестера.

### Шаг 6. Первый запуск

```
./tester -p config/config.json -s scripts/simple/channel_add.txt
```

Тестер напишет «Сервер запущен… ожидаем подключения», дождётся, пока MM подключится к нему, и выполнит сценарий. Успех — в конце строка про успешное завершение и ответы `RC_SUCCESS`.

Что запускать дальше:

| Что | Команда |
|---|---|
| Один звонок с настоящим звуком | `./tester -p config/config.json -s scripts/many_steps/call_rtp.txt` |
| Проверка кодеков и DTMF на MM | `./tester -p config/config.json -s scripts/mm_check` |
| Нагрузка: 100 одновременных звонков | `./tester -p config/config.json -s scripts/many_steps/call_rtp.txt --parallel 100 --rate 20` |

После звонка в папке появятся файлы `call_A_1.wav` и `call_B_2.wav` — это записанный звук, их можно послушать.
