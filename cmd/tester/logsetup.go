package main

import (
	"io"
	"os"
)

// logFile — файл из --log-file; nil, если флаг не задан
var logFile *os.File

// openLogFile открывает файл лога, обрезая его: каждый запуск пишет с чистого листа
func openLogFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	logFile = file

	return nil
}

// logOutput выбирает, куда писать лог: на терминал (stderr) и/или в файл --log-file.
// Без флага и без терминала (экран нагрузки) лог не пишется никуда.
func logOutput(toTerminal bool) io.Writer {
	var writers []io.Writer

	if toTerminal {
		writers = append(writers, os.Stderr)
	}

	if logFile != nil {
		writers = append(writers, logFile)
	}

	switch len(writers) {
	case 0:
		return io.Discard
	case 1:
		return writers[0]
	}

	return io.MultiWriter(writers...)
}
