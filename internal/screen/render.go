// Package screen — экран нагрузочного теста в терминале (в духе sipp): общая статистика сверху, ниже таблица команд
// сценария в порядке записи. Клавиши меняют нагрузку прямо во время теста.
//
// Render и HandleKey не зависят от терминала и проверяются тестами; работа с терминалом (режим ввода, перерисовка,
// чтение клавиш) — в terminal.go.
package screen

import (
	"fmt"
	"strings"
	"time"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/load"
)

const (
	// textWidth — под сколько символов отводится колонка с текстом команды
	textWidth = 30

	// maxErrorShown — сколько символов последней ошибки показывать
	maxErrorShown = 110
)

// View — всё, что нужно для одного кадра
type View struct {
	Scenario  string // путь к сценарию
	Calls     int    // сколько звонков запланировано; 0 — до остановки
	Counters  load.Counters
	CPS       float64 // целевая частота, которую задал пользователь
	ActualCPS float64 // фактическая частота за последние секунды
	Parallel  int     // потолок одновременных; 0 — без ограничения
	Paused    bool
	Stopping  bool // нажато q: новые звонки не запускаются, ждём идущих
	Finished  bool // тест закончен
	LogFile   string
	Rows      []cmdstats.RowStat
}

// Action — что нужно сделать по нажатой клавише, кроме изменения нагрузки
type Action int

const (
	ActionNone Action = iota
	ActionStop        // q: прекратить запуск новых звонков, дождаться идущих
	ActionQuit        // Q: выйти сразу
)

// HandleKey применяет клавишу к управлению нагрузкой и возвращает действие для вызывающего.
// Клавиши: + и - меняют cps на 1, * и / на 10, p ставит на паузу и снимает её, q — завершить, Q — выйти сразу.
func HandleKey(ctl *load.Control, key byte) Action {
	switch key {
	case '+', '=':
		ctl.AddCPS(1)
	case '-', '_':
		ctl.AddCPS(-1)
	case '*':
		ctl.AddCPS(10)
	case '/':
		ctl.AddCPS(-10)
	case 'p', 'P':
		ctl.TogglePause()
	case 'q':
		return ActionStop
	case 'Q':
		return ActionQuit
	}

	return ActionNone
}

// Render строит текст кадра
func Render(v View) string {
	var b strings.Builder

	c := v.Counters
	line := strings.Repeat("-", 72)

	fmt.Fprintf(&b, " Нагрузочный тест: %s\n", v.Scenario)
	fmt.Fprintf(&b, " Клавиши: [+] [-] cps ±1   [*] [/] cps ±10   [p] пауза   [q] завершить (дождаться идущих)   [Q] выйти сразу\n")
	fmt.Fprintf(&b, "%s\n", line)

	fmt.Fprintf(&b, " Состояние:    %s\n", stateText(v))
	fmt.Fprintf(&b, " Прошло:       %s\n", formatElapsed(c.Elapsed))
	fmt.Fprintf(&b, " cps:          задано %.1f, сейчас %.1f\n", v.CPS, v.ActualCPS)
	fmt.Fprintf(&b, " Звонки:       запущено %s, идёт сейчас %d (потолок: %s), завершено %d, с ошибками %d%s\n",
		outOf(c.Started, v.Calls), c.Running, parallelText(v.Parallel), c.Finished, c.Failed, failedPercent(c))
	fmt.Fprintf(&b, " Длительность: мин %s, средняя %s, макс %s\n", seconds(c.DurMin), seconds(c.DurAvg), seconds(c.DurMax))

	if c.LastError != "" {
		fmt.Fprintf(&b, " Последняя ошибка: %s\n", shorten(c.LastError))
	}

	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, " %-7s %-*s %6s %10s %7s\n", "Номер", textWidth, "Команда", "Идёт", "Выполнено", "Ошибок")

	for _, row := range v.Rows {
		fmt.Fprintf(&b, "%s\n", rowLine(row))
	}

	fmt.Fprintf(&b, "%s\n", line)

	if v.LogFile != "" {
		fmt.Fprintf(&b, " Лог тестера записывается в файл %s\n", v.LogFile)
	}

	return b.String()
}

func stateText(v View) string {
	switch {
	case v.Finished:
		return "тест завершён"
	case v.Stopping:
		return "ЗАВЕРШЕНИЕ: новые звонки не запускаются, ждём идущих"
	case v.Paused:
		return "ПАУЗА: новые звонки не запускаются (идущие продолжаются), [p] — продолжить"
	}

	return "идёт"
}

func rowLine(row cmdstats.RowStat) string {
	return fmt.Sprintf(" %-7s %-*s %6d %10d %7d", row.Label, textWidth, truncate(row.Text, textWidth), row.Running, row.Count, row.Failed)
}

func outOf(done int64, total int) string {
	if total > 0 {
		return fmt.Sprintf("%d из %d", done, total)
	}

	return fmt.Sprintf("%d (без предела)", done)
}

func parallelText(parallel int) string {
	if parallel > 0 {
		return fmt.Sprintf("%d", parallel)
	}

	return "нет"
}

func failedPercent(c load.Counters) string {
	if c.Finished == 0 {
		return ""
	}

	return fmt.Sprintf(" (%.1f%%)", float64(c.Failed)/float64(c.Finished)*100)
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

func formatElapsed(d time.Duration) string {
	total := int(d.Seconds())

	return fmt.Sprintf("%02d:%02d:%02d", total/3600, total%3600/60, total%60)
}

// truncate обрезает по числу символов (не байт) и ставит «…», если текст не поместился
func truncate(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}

	return string(runes[:width-1]) + "…"
}

func shorten(text string) string {
	text = strings.Join(strings.Fields(text), " ")

	return truncate(text, maxErrorShown)
}
