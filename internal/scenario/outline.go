package scenario

import (
	"strconv"
	"strings"

	"tester_mm/internal/cmdstats"
)

// Observer получает от исполнителя сведения о выполнении команд. Номер команды (label) тот же, что в логе:
// 1, 2, 2.1 внутри for и ветки then, 2.else.1 в ветке else. Для for и if сообщается выполнение всего блока.
// Реализует *cmdstats.Collector.
type Observer interface {
	Begin(label string)
	End(label string, err error)
}

// SetObserver подключает наблюдателя за выполнением команд
func (executor *Executor) SetObserver(observer Observer) {
	executor.observer = observer
}

// Outline возвращает команды сценария для таблицы экрана нагрузки, в порядке записи, с теми же номерами, что получают
// выполняемые команды. В таблицу попадают только действия: запросы к MM (метод proto), wait_event, connect(), sleep, pause и rtp_*.
// Присваивания, log, fail и сами блоки for/if не показываются, но команды внутри блоков показываются (без отступа).
func (s *Scenario) Outline() []cmdstats.Row {
	return outlineBlock(nil, "", s.Commands)
}

func outlineBlock(rows []cmdstats.Row, prefix string, commands []Command) []cmdstats.Row {
	for i, cmd := range commands {
		label := prefix + strconv.Itoa(i+1)

		if text := outlineText(cmd); text != "" {
			rows = append(rows, cmdstats.Row{Label: label, Text: text})
		}

		if cmd.IsFor {
			rows = outlineBlock(rows, label+".", cmd.Body)
		}

		if cmd.IsIf {
			rows = outlineBlock(rows, label+".", cmd.Body)
			rows = outlineBlock(rows, label+".else.", cmd.ElseBody)
		}
	}

	return rows
}

// outlineText — как команда называется в таблице; пусто, если её показывать не нужно
func outlineText(cmd Command) string {
	switch {
	case cmd.IsFor, cmd.IsIf, cmd.IsLog, cmd.IsFail, cmd.IsSimpleAssign:
		return ""
	case cmd.IsSleep, cmd.IsPause:
		return strings.TrimSpace(cmd.RawLine)
	case cmd.IsConnect:
		return "connect()"
	case cmd.IsWaitEvent:
		return "wait_event " + cmd.EventName
	case cmd.IsRTP:
		return cmd.RTPOp
	}

	return cmd.Name
}
