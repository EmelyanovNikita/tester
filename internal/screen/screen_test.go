package screen

import (
	"strings"
	"testing"
	"time"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/load"
	"tester_mm/internal/rtp"
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
		"scripts/call.txt", "00:01:23", "target 10.0, actual 9.8", "started 40 of 100", "running 12",
		"finished 28", "failed 2 (7.1%)", "RC_BAD_REQUEST", "tester.log", "[p] pause",
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

	if text := Render(v); !strings.Contains(text, "running\n") {
		t.Errorf("обычное состояние:\n%s", text)
	}

	v.Paused = true

	if text := Render(v); !strings.Contains(text, "PAUSED") {
		t.Errorf("пауза не показана:\n%s", text)
	}

	v.Stopping = true

	if text := Render(v); !strings.Contains(text, "STOPPING") {
		t.Errorf("завершение не показано:\n%s", text)
	}

	v.Finished = true

	if text := Render(v); !strings.Contains(text, "finished") {
		t.Errorf("конец теста не показан:\n%s", text)
	}
}

func TestRenderUnlimitedCalls(t *testing.T) {
	v := sampleView()
	v.Calls = 0

	if text := Render(v); !strings.Contains(text, "started 40 (no limit)") {
		t.Errorf("без предела:\n%s", text)
	}
}

// В таблице нет колонок с длительностью: только сколько идёт, сколько выполнено и сколько ошибок
func TestRenderTableColumns(t *testing.T) {
	text := Render(sampleView())

	for _, unwanted := range []string{"min, s", "avg, s", "max, s"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("в таблице не должно быть колонки %q", unwanted)
		}
	}

	line := rowLine(cmdstats.RowStat{Row: cmdstats.Row{Label: "2", Text: "ChannelAdd"}, Stat: cmdstats.Stat{Running: 3, Count: 37, Failed: 2}}, 3)
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

// Колонки не «уезжают», даже если есть длинные номера вроде 21.else.1
func TestRenderAlignsColumnsWithLongLabels(t *testing.T) {
	v := sampleView()
	v.Rows = []cmdstats.RowStat{
		{Row: cmdstats.Row{Label: "2", Text: "rtp_open"}},
		{Row: cmdstats.Row{Label: "21.else.1", Text: "rtp_start"}},
	}

	var positions []int

	for _, line := range strings.Split(Render(v), "\n") {
		if strings.Contains(line, "rtp_open") || strings.Contains(line, "rtp_start") || strings.Contains(line, "Command") {
			positions = append(positions, strings.Index(line, "rtp_open")+strings.Index(line, "rtp_start")+strings.Index(line, "Command")+2)
		}
	}

	if len(positions) != 3 || positions[0] != positions[1] || positions[1] != positions[2] {
		t.Errorf("колонка «Command» не выровнена: %v", positions)
	}
}

func TestRenderRTPBlock(t *testing.T) {
	v := sampleView()

	if text := Render(v); strings.Contains(text, "RTP") {
		t.Errorf("без HasRTP блока RTP быть не должно:\n%s", text)
	}

	v.HasRTP = true
	v.InRate, v.OutRate = 1500, 1600
	v.RTP = rtp.Totals{SessionsRunning: 10, SessionsSilent: 2, SessionsFinished: 5, FinishedSilent: 1,
		SentPackets: 9000, IncomingPackets: 8000, AudioPackets: 7990, DTMFPackets: 4, OtherPackets: 3, BadPackets: 3, LostPackets: 7, MaxJitterMs: 1.25}

	text := Render(v)

	for _, want := range []string{"running 10", "no incoming traffic 2", "MM sends nothing", "finished 5 (no traffic 1)",
		"8000 pkt (1500/s)", "audio 7990", "lost 7", "max jitter 1.2", "9000 pkt (1600/s)"} {
		if !strings.Contains(text, want) {
			t.Errorf("в блоке RTP нет %q:\n%s", want, text)
		}
	}

	v.RTP.SessionsSilent = 0

	if text := Render(v); strings.Contains(text, "MM sends nothing") {
		t.Errorf("предупреждение не должно показываться, когда трафик идёт везде:\n%s", text)
	}
}

func TestSpeedHistoryNeverNegative(t *testing.T) {
	h := newSpeedHistory()
	now := time.Now()

	h.add(now, 100)

	if got := h.add(now.Add(time.Second), 50); got != 0 {
		t.Errorf("скорость при убывающем счётчике: %v, ожидалось 0", got)
	}
}
