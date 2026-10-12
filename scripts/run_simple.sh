#!/bin/sh
# Пример 2: прогон всех простых сценариев из scripts/simple по очереди (по одному вызову или проверке на сценарий).
# --continue-on-error: ошибка в одной команде не останавливает остальные команды сценария.
# В конце тестер печатает итог по каждому сценарию; код возврата 1, если были ошибки.
# Использование: ./scripts/run_simple.sh [config/config.json] [дополнительные флаги тестера]
cd "$(dirname "$0")/.." || exit 1
. scripts/_tester.sh
CONFIG="${1:-config/config.json}"
[ $# -gt 0 ] && shift

run_tester -p "$CONFIG" --continue-on-error -s scripts/simple "$@"
