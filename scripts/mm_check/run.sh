#!/bin/sh
# Прогон проверок на реальном MM: ./scripts/mm_check/run.sh [config/config.json]
# Каждый сценарий запускается отдельно; код возврата 0, только если все ОК.
cd "$(dirname "$0")/../.." || exit 1
CONFIG="${1:-config/config.json}"
go build -o /tmp/tester_mm_check ./cmd/tester || exit 1
fail=0
for f in scripts/mm_check/*.txt; do
    out=$(/tmp/tester_mm_check -p "$CONFIG" -s "$f" --continue-on-error 2>&1)
    echo "$out" | grep -E "ОК |ПРОВАЛ|ОШИБКА|: кодек|dtmf:"
    if ! echo "$out" | grep -q "ОК " || echo "$out" | grep -q "ПРОВАЛ"; then
        echo "=== $f: ПРОВАЛ"
        fail=1
    else
        echo "=== $f: ОК"
    fi
done
exit $fail
