### Краткое руководство по тестеру MM-а

пока только две более менее рабочие версии, причем как запускать я знаю только вторую:

имеем адрес и порт сервера куда хотим отправить запрос с указанными параметрами
```
go build -o tester main.go
./tester -address 127.0.0.1 -port 7001 -cmd "ChannelAdd(session_id:\"1\",context_id:1,channel_id:1,need_media:true,sdp_offer:\"\",media_profile_id:1,site:\"NSK\")"
2026/07/05 23:22:33 Подключение к 127.0.0.1:7001
2026/07/05 23:22:33 Найден метод: /mm.server_api.ChannelService/ChannelAdd
2026/07/05 23:22:33 Запрос: {"sessionId":"1","contextId":"1","channelId":"1","needMedia":true,"mediaProfileId":"1","site":"NSK"}
2026/07/05 23:22:33 Отправка запроса...
2026/07/05 23:22:33 Ответ: {"replyCode":"RC_UNKNOWN_SITE"}
```

```
./tester -address 127.0.0.1 -port 7001 -cmd "ChannelAdd(session_id:\"1\",context_id:1,channel_id:1,need_media:true,sdp_offer:\"\",media_profile_id:1,site:\"NSK\")"

2026/07/05 23:24:43 Подключение к 127.0.0.1:7001
2026/07/05 23:24:43 Найден метод: /mm.server_api.ChannelService/ChannelAdd
2026/07/05 23:24:43 Запрос: {"sessionId":"1","contextId":"1","channelId":"1","needMedia":true,"mediaProfileId":"1","site":"NSK"}
2026/07/05 23:24:43 Отправка запроса...
2026/07/05 23:24:43 Ответ: {"replyCode":"RC_UNKNOWN_SITE"}
```
