#!/bin/sh
# Пример 1: один полный звонок с настоящим RTP (две ноги, тон, DTMF, проверки).
# Принятый звук сохраняется в call_A_1.wav и call_B_2.wav в текущей папке.
# Использование: ./scripts/run_call.sh [config/config.json] [дополнительные флаги тестера]
cd "$(dirname "$0")/.." || exit 1
. scripts/_tester.sh
CONFIG="${1:-config/config.json}"
[ $# -gt 0 ] && shift

run_tester -p "$CONFIG" -s scripts/many_steps/call_rtp.txt "$@"
