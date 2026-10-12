package scenario

import (
	"fmt"
	"log"
	"sort"
	"strconv"

	"tester_mm/internal/rtp"
)

// Параллельный запуск: один сценарий выполняют много исполнителей (клиентов) одновременно.
// У каждого свои переменные, RTP-сеансы и счётчики; соединение с MM, сервер событий и фабрика портов общие.

// InstanceVar — переменная сценария с номером клиента (с 1); по ней можно различать клиентов
const InstanceVar = "INSTANCE"

// SetInstance задаёт номер клиента для переменной INSTANCE. Если prefixLogs — номер добавляется и в начало
// каждой строки лога ("[#17] ..."): это нужно, когда клиентов несколько и строки разных клиентов перемешиваются.
func (executor *Executor) SetInstance(n int, prefixLogs bool) {
	executor.instance = n
	executor.exprParser.vars.Set(InstanceVar, strconv.Itoa(n))

	if prefixLogs {
		executor.logPrefix = fmt.Sprintf("[#%d] ", n)
	} else {
		executor.logPrefix = ""
	}
}

// SetQuiet отключает подробный лог (команды, запросы, ответы). Остаются вывод log(...) и ошибки.
func (executor *Executor) SetQuiet(quiet bool) {
	executor.quiet = quiet
}

// logf пишет строку лога всегда
func (executor *Executor) logf(format string, args ...interface{}) {
	log.Print(executor.logPrefix + fmt.Sprintf(format, args...))
}

// tracef пишет подробную строку лога, если не включён тихий режим
func (executor *Executor) tracef(format string, args ...interface{}) {
	if executor.quiet {
		return
	}

	executor.logf(format, args...)
}

// noteFinal запоминает итоговые счётчики остановленного сеанса; сеансы, которые не запускались, не учитываются
func (executor *Executor) noteFinal(id string, stats rtp.Stats) {
	if stats.Codec == "" {
		return
	}

	if executor.rtpFinal == nil {
		executor.rtpFinal = map[string]rtp.Stats{}
	}

	if _, done := executor.rtpFinal[id]; !done {
		executor.rtpFinal[id] = stats
	}
}

// RTPStats возвращает итоговые счётчики всех остановленных за сценарий RTP-сеансов (по порядку номеров)
func (executor *Executor) RTPStats() []rtp.Stats {
	ids := make([]string, 0, len(executor.rtpFinal))
	for id := range executor.rtpFinal {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	result := make([]rtp.Stats, 0, len(ids))
	for _, id := range ids {
		result = append(result, executor.rtpFinal[id])
	}

	return result
}
