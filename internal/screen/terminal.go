package screen

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/load"
)

const (
	redrawEvery = 500 * time.Millisecond

	// speedWindow — за какое время считается фактический cps
	speedWindow = 5 * time.Second

	clearScreen = "\x1b[H\x1b[2J"
	cursorHome  = "\x1b[H"
	clearToEnd  = "\x1b[J"
	clearLine   = "\x1b[K"
)

// Session — терминал в режиме посимвольного ввода и поток нажатых клавиш.
// Создаётся один раз на весь тест; Close возвращает терминал в обычный режим.
type Session struct {
	saved string // настройки терминала до изменения (вывод stty -g)
	keys  chan byte
	out   io.Writer
	once  sync.Once
}

// NeedsTerminal проверяет, что ввод идёт с терминала: экран и клавиши без него не работают
func NeedsTerminal() error {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("экран нагрузки требует запуска в терминале (ввод не с клавиатуры)")
	}

	return nil
}

// Start переводит терминал в режим, где клавиши приходят сразу, без Enter и без эха. Ctrl+C продолжает работать.
// Использует команду stty (Linux, macOS).
func Start() (*Session, error) {
	if err := NeedsTerminal(); err != nil {
		return nil, err
	}

	saved, err := stty("-g")
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать настройки терминала (stty -g): %w", err)
	}

	if _, err := stty("-icanon", "-echo", "min", "1"); err != nil {
		return nil, fmt.Errorf("не удалось переключить терминал в посимвольный режим (stty): %w", err)
	}

	s := &Session{saved: strings.TrimSpace(saved), keys: make(chan byte, 16), out: os.Stdout}

	go s.readKeys()

	return s, nil
}

func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin

	out, err := cmd.Output()

	return string(out), err
}

// readKeys читает ввод до конца процесса (чтение из stdin нельзя прервать) и отдаёт клавиши в канал
func (s *Session) readKeys() {
	buf := make([]byte, 1)

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}

		if n == 1 {
			select {
			case s.keys <- buf[0]:
			default: // никто не читает: клавиша теряется
			}
		}
	}
}

// Close возвращает терминал в обычный режим; можно вызывать несколько раз
func (s *Session) Close() {
	s.once.Do(func() {
		_, _ = stty(s.saved)
	})
}

// Show рисует экран теста, пока не закрыт done, и передаёт клавиши управлению. Обновление раз в полсекунды.
// onAction вызывается для q и Q. После done рисуется последний кадр с пометкой «тест завершён» и остаётся на экране.
func (s *Session) Show(ctl *load.Control, collector *cmdstats.Collector, base View, done <-chan struct{}, onAction func(Action)) {
	history := newSpeedHistory()
	stopping := false

	draw := func(finished bool) {
		view := base
		view.Counters = ctl.Counters()
		view.CPS = ctl.CPS()
		view.Paused = ctl.Paused()
		view.Stopping = stopping
		view.Finished = finished
		view.Rows = collector.Snapshot()
		view.ActualCPS = history.add(time.Now(), view.Counters.Started)

		fmt.Fprint(s.out, cursorHome+strings.ReplaceAll(Render(view), "\n", clearLine+"\n")+clearToEnd)
	}

	fmt.Fprint(s.out, clearScreen)

	ticker := time.NewTicker(redrawEvery)
	defer ticker.Stop()

	draw(false)

	for {
		select {
		case <-done:
			draw(true)

			return
		case key := <-s.keys:
			action := HandleKey(ctl, key)

			if action == ActionStop {
				stopping = true
			}

			if action != ActionNone {
				onAction(action)
			}

			draw(false)
		case <-ticker.C:
			draw(false)
		}
	}
}

// speedHistory считает фактический cps как прирост числа запущенных звонков за последние speedWindow секунд
type speedHistory struct {
	times  []time.Time
	starts []int64
}

func newSpeedHistory() *speedHistory {
	return &speedHistory{}
}

// add запоминает отсчёт и возвращает скорость по окну
func (h *speedHistory) add(now time.Time, started int64) float64 {
	h.times = append(h.times, now)
	h.starts = append(h.starts, started)

	for len(h.times) > 2 && now.Sub(h.times[1]) >= speedWindow {
		h.times = h.times[1:]
		h.starts = h.starts[1:]
	}

	span := now.Sub(h.times[0]).Seconds()
	if span <= 0 {
		return 0
	}

	return float64(started-h.starts[0]) / span
}
