#!/usr/bin/env bash
# Установка и первичная настройка тестера «с нуля» (Linux).
# Положите этот файл в папку тестера (куда сделали git clone) и запустите:
#     bash setup.sh
# Скрипт: проверит/поставит Go, спросит путь к proto-файлам MM и адрес этого компьютера,
# запишет config/config.json, соберёт тестер и прогонит его собственные тесты.
# Права администратора не нужны: Go ставится в ~/go-sdk.

set -e
cd "$(dirname "$0")"

GO_VERSION="1.22.12"
NEED_MAJOR=1
NEED_MINOR=22

say() { printf '\n== %s\n' "$*"; }
die() { printf '\nОШИБКА: %s\n' "$*" >&2; exit 1; }

[ -f go.mod ] && [ -d cmd/tester ] || die "запустите скрипт из папки тестера (там должны быть go.mod и cmd/tester)"

# ---------- 1. Go ----------
go_ok() {
    command -v go >/dev/null 2>&1 || return 1
    local v major minor
    v=$(go env GOVERSION 2>/dev/null | sed 's/^go//') || return 1
    major=${v%%.*}
    minor=${v#*.}; minor=${minor%%.*}
    [ "$major" -gt "$NEED_MAJOR" ] || { [ "$major" -eq "$NEED_MAJOR" ] && [ "$minor" -ge "$NEED_MINOR" ]; }
}

say "Шаг 1. Проверка Go"
if [ -x "$HOME/go-sdk/go/bin/go" ]; then
    export PATH="$HOME/go-sdk/go/bin:$PATH"
fi

if go_ok; then
    echo "Go найден: $(go version)"
else
    echo "Go версии $NEED_MAJOR.$NEED_MINOR или новее не найден, устанавливаю в ~/go-sdk"
    case "$(uname -m)" in
        x86_64)         arch=amd64 ;;
        aarch64|arm64)  arch=arm64 ;;
        *) die "неизвестная архитектура $(uname -m): установите Go вручную с https://go.dev/dl/" ;;
    esac
    file="go${GO_VERSION}.linux-${arch}.tar.gz"
    mkdir -p "$HOME/go-sdk"
    if command -v curl >/dev/null 2>&1; then
        curl -fL -o "$HOME/go-sdk/$file" "https://go.dev/dl/$file" || die "не удалось скачать $file (нет интернета?)"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$HOME/go-sdk/$file" "https://go.dev/dl/$file" || die "не удалось скачать $file (нет интернета?)"
    else
        die "нужен curl или wget: sudo apt install curl"
    fi
    rm -rf "$HOME/go-sdk/go"
    tar -C "$HOME/go-sdk" -xzf "$HOME/go-sdk/$file" || die "не удалось распаковать $file"
    rm -f "$HOME/go-sdk/$file"
    export PATH="$HOME/go-sdk/go/bin:$PATH"
    if ! grep -q 'go-sdk/go/bin' "$HOME/.bashrc" 2>/dev/null; then
        echo 'export PATH="$HOME/go-sdk/go/bin:$PATH"' >> "$HOME/.bashrc"
    fi
    go_ok || die "Go установлен, но не запускается"
    echo "Установлено: $(go version)"
fi

# ---------- 2. Конфиг ----------
say "Шаг 2. Настройка config/config.json"

current_proto=""
if [ -f config/config.json ]; then
    current_proto=$(sed -n 's/.*"proto_root"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' config/config.json | head -1)
fi

echo "Нужна папка с proto-файлами media-manager (репозиторий ecss-media-manager-api-proto)."
echo "В ней должен лежать файл mm/client_api/v1/mm_client_api.proto."
while true; do
    if [ -n "$current_proto" ] && [ -d "$current_proto" ]; then
        read -r -p "Путь к папке с proto [$current_proto]: " proto_root
        proto_root=${proto_root:-$current_proto}
    else
        read -r -p "Путь к папке с proto: " proto_root
    fi
    proto_root=${proto_root/#\~/$HOME}
    if [ -f "$proto_root/mm/client_api/v1/mm_client_api.proto" ]; then
        break
    fi
    echo "  Не нашёл $proto_root/mm/client_api/v1/mm_client_api.proto — проверьте путь и попробуйте снова."
done

default_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "Адрес этого компьютера, по которому MM сможет присылать звук (RTP)."
read -r -p "IP этого компьютера [${default_ip:-нет}]: " rtp_ip
rtp_ip=${rtp_ip:-$default_ip}
[ -n "$rtp_ip" ] || die "IP не задан"

read -r -p "Порт, на котором тестер ждёт MM [7000]: " port
port=${port:-7000}
case "$port" in ''|*[!0-9]*) die "порт должен быть числом" ;; esac

[ -f config/config.json ] && cp config/config.json config/config.json.bak && echo "Старый конфиг сохранён: config/config.json.bak"

mkdir -p config
cat > config/config.json <<EOF
{
	"proto_root": "$proto_root",
	"proto_files": [
		"mm/client_api/v1/mm_client_api.proto",
		"mm/objects/v1/mm_objects.proto",
		"mm/server_api/v1/mm_server_api.proto"
	],
	"local_server": {
		"address": "0.0.0.0",
		"port": $port
	},
	"rtp": {
		"ip": "$rtp_ip"
	}
}
EOF
echo "Записан config/config.json"

# ---------- 3. Сборка ----------
say "Шаг 3. Сборка (в первый раз скачивает библиотеки, 1-2 минуты)"
go build -o tester ./cmd/tester || die "сборка не удалась (нет интернета для скачивания библиотек?)"
echo "Готово: ./tester"

# ---------- 4. Тесты ----------
say "Шаг 4. Проверка (тесты не требуют MM)"
go test ./... || die "тесты не прошли"

# ---------- Итог ----------
say "Всё готово"
cat <<EOF
Осталось указать в настройках MM, что управляющий сервер (SSW) — это
    $rtp_ip:$port
(как это сделать — зависит от вашей установки MM, уточните у администратора MM).

Первый запуск:
    ./tester -p config/config.json -s scripts/simple/channel_add.txt

Один звонок со звуком:
    ./tester -p config/config.json -s scripts/many_steps/call_rtp.txt

Если Go был установлен этим скриптом, откройте новое окно терминала (или выполните: source ~/.bashrc).
EOF
