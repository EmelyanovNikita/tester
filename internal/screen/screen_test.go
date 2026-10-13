package screen

import (
	"strings"
	"testing"
	"time"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/load"
)

func TestHandleKeyChangesCPS(t *testing.T) {
	ctl := load.NewControl(10)

	cases := []struct {
		key  byte
		want float64
	}{
		{'+', 11}, {'=', 12}, {'-', 11}, {'*', 21}, {'/', 11}, {'/', 1}, {'-', 0.1}, {'-', 0.1},
	}

	for _, c := range cases {
		if action := HandleKey(ctl, c.key); action != ActionNone {
			t.Errorf("клавиша %q вернула действие %v", c.key, action)
		}

		if got := ctl.CPS(); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("после %q cps %v, ожидалось %v", c.key, got, c.want)
		}
	}
}

func TestHandleKeyPauseAndQuit(t *testing.T) {
	ctl := load.NewControl(5)

	HandleKey(ctl, 'p')

	if !ctl.Paused() {
		t.Error("p должна ставить на паузу")
	}

	HandleKey(ctl, 'p')

	if ctl.Paused() {
		t.Error("повторная p должна снимать паузу")
	}

	if HandleKey(ctl, 'q') != ActionStop || HandleKey(ctl, 'Q') != ActionQuit {
		t.Error("q — завершение, Q — немедленный выход")
	}

	if HandleKey(ctl, 'x') != ActionNone || ctl.CPS() != 5 {
		t.Error("прочие клавиши ничего не меняют")
	}
}

func sampleView() View {
	return View{
		Scenario: "scripts/call.txt",
		Calls:    100,
		CPS:      10,
		Counters: load.Counters{Elapsed: 83 * time.Second, Started: 40, Running: 12, Finished: 28, Failed: 2,
			DurMin: time.Second, DurAvg: 2 * time.Second, DurMax: 3 * time.Second, LastError: "ChannelAdd:   RC_BAD_REQUEST"},
		ActualCPS: 9.8,
		Rows: []cmdstats.RowStat{
			{Row: cmdstats.Row{Label: "1", Text: "connect()"}, Stat: cmdstats.Stat{Count: 40}},
			{Row: cmdstats.Row{Label: "2", Text: "ChannelAdd"}, Stat: cmdstats.Stat{Running: 3, Count: 37, Failed: 2}},
			{Row: cmdstats.Row{Label: "3", Text: "sleep(5)"}, Stat: cmdstats.Stat{Running: 12, Count: 5, Failed: 1}},
		},
		LogFile: "tester.log",
	}
}

func TestRenderShowsStatsAndRowsInOrder(t *testing.T) {
	text := Render(sampleView())

	for _, want := range []string{
		"scripts/call.txt", "00:01:23", "задано 10.0, сейчас 9.8", "запущено 40 из 100", "идёт сейчас 12",
		"завершено 28", "с ошибками 2 (7.1%)", "RC_BAD_REQUEST", "tester.log", "[p] пауза",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в кадре нет %q:\n%s", want, text)
		}
	}

	first, second, third := strings.Index(text, "connect()"), strings.LastIndex(text, "ChannelAdd"), strings.Index(text, "sleep(5)")
	if !(first > 0 && first < second && second < third) {
		t.Errorf("команды идут не в порядке сценария:\n%s", text)
	}
}

func TestRenderStates(t *testing.T) {
	v := sampleView()

	if text := Render(v); !strings.Contains(text, "идёт\n") {
		t.Errorf("обычное состояние:\n%s", text)
	}

	v.Paused = true

	if text := Render(v); !strings.Contains(text, "ПАУЗА") {
		t.Errorf("пауза не показана:\n%s", text)
	}

	v.Stopping = true

	if text := Render(v); !strings.Contains(text, "ЗАВЕРШЕНИЕ") {
		t.Errorf("завершение не показано:\n%s", text)
	}

	v.Finished = true

	if text := Render(v); !strings.Contains(text, "тест завершён") {
		t.Errorf("конец теста не показан:\n%s", text)
	}
}

func TestRenderUnlimitedCalls(t *testing.T) {
	v := sampleView()
	v.Calls = 0

	if text := Render(v); !strings.Contains(text, "запущено 40 (без предела)") {
		t.Errorf("без предела:\n%s", text)
	}
}

// В таблице нет колонок с длительностью: только сколько идёт, сколько выполнено и сколько ошибок
func TestRenderTableColumns(t *testing.T) {
	text := Render(sampleView())

	for _, unwanted := range []string{"мин, с", "сред, с", "макс, с"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("в таблице не должно быть колонки %q", unwanted)
		}
	}

	line := rowLine(cmdstats.RowStat{Row: cmdstats.Row{Label: "2", Text: "ChannelAdd"}, Stat: cmdstats.Stat{Running: 3, Count: 37, Failed: 2}})
	for _, want := range []string{"2", "ChannelAdd", "3", "37"} {
		if !strings.Contains(line, want) {
			t.Errorf("в строке %q нет %q", line, want)
		}
	}
}

func TestTruncateByRunes(t *testing.T) {
	if got := truncate("абвгде", 4); got != "абв…" {
		t.Errorf("усечение: %q", got)
	}

	if got := truncate("абв", 4); got != "абв" {
		t.Errorf("короткий текст изменён: %q", got)
	}
}

func TestSpeedHistory(t *testing.T) {
	h := newSpeedHistory()
	now := time.Now()

	h.add(now, 0)

	if got := h.add(now.Add(2*time.Second), 20); got < 9.9 || got > 10.1 {
		t.Errorf("скорость %v, ожидалось 10", got)
	}

	// старые отсчёты уходят из окна: за последние 5 с запущено 5 звонков
	h.add(now.Add(10*time.Second), 120)

	if got := h.add(now.Add(15*time.Second), 125); got < 0.9 || got > 1.1 {
		t.Errorf("скорость по окну %v, ожидалось около 1", got)
	}
}
