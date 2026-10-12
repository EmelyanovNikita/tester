#!/bin/sh
# Общая часть примеров: функция run_tester запускает ./tester (если собран), иначе go run.
# Не запускается сама по себе; подключается из run_*.sh после перехода в корень репозитория.
run_tester() {
    if [ -x ./tester ]; then
        ./tester "$@"
    else
        go run ./cmd/tester "$@"
    fi
}
