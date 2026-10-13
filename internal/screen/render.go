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
	"tester_mm/internal/rtp"
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

	// RTP — суммарные счётчики потоков; блок показывается, только если HasRTP. InRate и OutRate — пакетов в секунду.
	HasRTP  bool
	RTP     rtp.Totals
	InRate  float64
	OutRate float64
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

// Render строит текст кадра. Надписи на экране английские, как у sipp.
func Render(v View) string {
	var b strings.Builder

	c := v.Counters
	line := strings.Repeat("-", 76)

	fmt.Fprintf(&b, " Load test: %s\n", v.Scenario)
	fmt.Fprintf(&b, " Keys: [+] [-] cps +-1   [*] [/] cps +-10   [p] pause   [q] stop (wait for running calls)   [Q] quit now\n")
	fmt.Fprintf(&b, "%s\n", line)

	fmt.Fprintf(&b, " State:      %s\n", stateText(v))
	fmt.Fprintf(&b, " Elapsed:    %s\n", formatElapsed(c.Elapsed))
	fmt.Fprintf(&b, " CPS:        target %.1f, actual %.1f\n", v.CPS, v.ActualCPS)
	fmt.Fprintf(&b, " Calls:      started %s, running %d (max parallel: %s), finished %d, failed %d%s\n",
		outOf(c.Started, v.Calls), c.Running, parallelText(v.Parallel), c.Finished, c.Failed, failedPercent(c))
	fmt.Fprintf(&b, " Call time:  min %s s, avg %s s, max %s s\n", seconds(c.DurMin), seconds(c.DurAvg), seconds(c.DurMax))

	if c.LastError != "" {
		fmt.Fprintf(&b, " Last error: %s\n", shorten(c.LastError))
	}

	if v.HasRTP {
		r := v.RTP

		note := ""
		if r.SessionsSilent > 0 {
			note = "  <-- MM sends nothing to these"
		}

		fmt.Fprintf(&b, " RTP streams: running %d, no incoming traffic %d%s, finished %d (no traffic %d)\n",
			r.SessionsRunning, r.SessionsSilent, note, r.SessionsFinished, r.FinishedSilent)
		fmt.Fprintf(&b, " RTP in:      %d pkt (%.0f/s): audio %d, dtmf %d, other %d, bad %d; lost %d, max jitter %.1f ms\n",
			r.IncomingPackets, v.InRate, r.AudioPackets, r.DTMFPackets, r.OtherPackets, r.BadPackets, r.LostPackets, r.MaxJitterMs)
		fmt.Fprintf(&b, " RTP out:     %d pkt (%.0f/s)\n", r.SentPackets, v.OutRate)
	}

	// колонка с номером шире самого длинного номера, иначе таблица «уезжает»
	labelWidth := len("No.")
	for _, row := range v.Rows {
		if n := len(row.Label); n > labelWidth {
			labelWidth = n
		}
	}

	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, " %-*s  %-*s %8s %9s %8s\n", labelWidth, "No.", textWidth, "Command", "Running", "Done", "Failed")

	for _, row := range v.Rows {
		fmt.Fprintf(&b, "%s\n", rowLine(row, labelWidth))
	}

	fmt.Fprintf(&b, "%s\n", line)

	if v.LogFile != "" {
		fmt.Fprintf(&b, " Tester log is written to %s\n", v.LogFile)
	}

	return b.String()
}

func stateText(v View) string {
	switch {
	case v.Finished:
		return "finished"
	case v.Stopping:
		return "STOPPING: no new calls, waiting for running calls to end"
	case v.Paused:
		return "PAUSED: no new calls (running calls continue), press [p] to resume"
	}

	return "running"
}

func rowLine(row cmdstats.RowStat, labelWidth int) string {
	return fmt.Sprintf(" %-*s  %-*s %8d %9d %8d", labelWidth, row.Label, textWidth, truncate(row.Text, textWidth), row.Running, row.Count, row.Failed)
}

func outOf(done int64, total int) string {
	if total > 0 {
		return fmt.Sprintf("%d of %d", done, total)
	}

	return fmt.Sprintf("%d (no limit)", done)
}

func parallelText(parallel int) string {
	if parallel > 0 {
		return fmt.Sprintf("%d", parallel)
	}

	return "none"
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
