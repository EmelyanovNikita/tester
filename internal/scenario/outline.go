package scenario

import (
	"strconv"
	"strings"
	"time"

	"tester_mm/internal/cmdstats"
)

// Observer получает от исполнителя сведения о выполнении команд. Номер команды (label) тот же, что в логе:
// 1, 2, 2.1 внутри for и ветки then, 2.else.1 в ветке else. Для for и if сообщается выполнение всего блока.
// Реализует *cmdstats.Collector.
type Observer interface {
	Begin(label string)
	End(label string, elapsed time.Duration, err error)
}

// SetObserver подключает наблюдателя за выполнением команд
func (executor *Executor) SetObserver(observer Observer) {
	executor.observer = observer
}

// Outline возвращает все команды сценария в порядке записи, с теми же номерами, что получают выполняемые команды.
// Вложенные в for и if команды идут сразу после своего блока с отступом на уровень глубже; ветка else начинается строкой else.
func (s *Scenario) Outline() []cmdstats.Row {
	return outlineBlock(nil, "", 0, s.Commands)
}

func outlineBlock(rows []cmdstats.Row, prefix string, depth int, commands []Command) []cmdstats.Row {
	for i, cmd := range commands {
		label := prefix + strconv.Itoa(i+1)

		rows = append(rows, cmdstats.Row{Label: label, Depth: depth, Text: outlineText(cmd)})

		if cmd.IsFor {
			rows = outlineBlock(rows, label+".", depth+1, cmd.Body)
		}

		if cmd.IsIf {
			rows = outlineBlock(rows, label+".", depth+1, cmd.Body)

			if len(cmd.ElseBody) > 0 {
				rows = append(rows, cmdstats.Row{Label: label + ".else", Depth: depth, Text: "else"})
				rows = outlineBlock(rows, label+".else.", depth+1, cmd.ElseBody)
			}
		}
	}

	return rows
}

// outlineText — строка команды как в сценарии, без открывающей скобки блока
func outlineText(cmd Command) string {
	text := strings.TrimSpace(cmd.RawLine)

	if cmd.IsFor || cmd.IsIf {
		text = strings.TrimSpace(strings.TrimSuffix(text, "{"))
	}

	return text
}
