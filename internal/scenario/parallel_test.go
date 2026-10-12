package scenario

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"tester_mm/internal/load"
	"tester_mm/internal/rtp"
)

// captureLog перехватывает лог на время теста
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	log.SetOutput(&lockedWriter{w: &buf})

	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	return &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.w.Write(p)
}

func TestInstanceVariableAndPrefix(t *testing.T) {
	buf := captureLog(t)
	executor, parser := newRTPExecutor(t)
	executor.SetInstance(17, true)

	if err := runRTPScript(t, executor, parser, "me = INSTANCE\nlog(\"я клиент\", INSTANCE)\n"); err != nil {
		t.Fatal(err)
	}

	if v, _ := varValue(executor, "me"); v != "17" {
		t.Errorf("INSTANCE = %q", v)
	}

	if !strings.Contains(buf.String(), "[#17] [2] log: я клиент 17") {
		t.Errorf("в логе нет строки с приставкой клиента:\n%s", buf.String())
	}
}

// В тихом режиме остаются только log(...) и ошибки
func TestInstanceWithoutPrefix(t *testing.T) {
	buf := captureLog(t)
	executor, parser := newRTPExecutor(t)
	executor.SetInstance(1, false)

	if err := runRTPScript(t, executor, parser, "log(\"привет\", INSTANCE)\n"); err != nil {
		t.Fatal(err)
	}

	if out := buf.String(); strings.Contains(out, "[#1]") || !strings.Contains(out, "[1] log: привет 1") {
		t.Errorf("приставки быть не должно:\n%s", out)
	}
}

func TestQuietMode(t *testing.T) {
	buf := captureLog(t)
	executor, parser := newRTPExecutor(t)
	executor.SetInstance(3, true)
	executor.SetQuiet(true)
	executor.ContinueOnError = true

	err := runRTPScript(t, executor, parser, "x = 5\nsleep(0)\nlog(\"видно\", x)\nbroken = nothing_here\n")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	out := buf.String()

	for _, want := range []string{"[#3] [3] log: видно 5", "[#3] [4] ОШИБКА"} {
		if !strings.Contains(out, want) {
			t.Errorf("в тихом режиме пропало %q:\n%s", want, out)
		}
	}

	for _, unwanted := range []string{"x = 5", "sleep 0", "Сценарий выполнен"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("в тихом режиме не должно быть %q:\n%s", unwanted, out)
		}
	}
}

func TestFailCommand(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	err := runRTPScript(t, executor, parser, "n = 5\nfail(\"потери\", n, \"%\")\nafter = 1\n")
	if err == nil || !strings.Contains(err.Error(), "fail: потери 5 %") || !strings.Contains(err.Error(), "команде 2") {
		t.Fatalf("ошибка: %v", err)
	}

	if _, ok := varValue(executor, "after"); ok {
		t.Error("после fail сценарий должен остановиться")
	}

	// с ContinueOnError сценарий идёт дальше, но завершается ошибкой
	executor, parser = newRTPExecutor(t)
	executor.ContinueOnError = true

	err = runRTPScript(t, executor, parser, "fail(\"плохо\")\nafter = 1\n")
	if err == nil || !strings.Contains(err.Error(), "команд с ошибками: 1") {
		t.Errorf("ошибка: %v", err)
	}

	if v, _ := varValue(executor, "after"); v != "1" {
		t.Error("с ContinueOnError сценарий должен продолжиться")
	}
}

func TestParseFailErrors(t *testing.T) {
	data := loadTestProto(t)

	for text, want := range map[string]string{
		"fail\n":       "",
		"fail()\n":     "fail без аргументов",
		"fail(a, )\n":  "пустой аргумент",
		"fail(\"x\"\n": "неверный синтаксис fail",
	} {
		_, err := NewParser(data.Methods).Parse(text)

		if want == "" {
			continue // «fail» без скобок — обычное выражение, его разбор не относится к этому тесту
		}

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ошибка %v, ожидалась с %q", text, err, want)
		}
	}

	// переменная с именем fail остаётся переменной
	s := mustParse(t, data, "fail = 5\nx = fail\n")
	if len(s.Commands) != 2 || s.Commands[0].IsFail || !s.Commands[0].IsSimpleAssign {
		t.Errorf("fail = 5 должно быть присваиванием: %+v", s.Commands[0])
	}
}

// Итоговые счётчики попадают в RTPStats как после rtp_stop, так и после автоматической остановки; сеанс без rtp_start не считается
func TestRTPStatsCollected(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	script := `
a = rtp_open(codecs = "PCMA")
b = rtp_open(codecs = "PCMA")
c = rtp_open()
rtp_start(a, b.sdp)
rtp_start(b, a.sdp)
sleep(0.2)
sa = rtp_stop(a)
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	stats := executor.RTPStats()
	if len(stats) != 2 {
		t.Fatalf("учтено %d сеансов, ожидалось 2 (a — rtp_stop, b — автоостановка; c не запускался)", len(stats))
	}

	for i, st := range stats {
		if st.SentPackets == 0 || st.ReceivedPackets == 0 {
			t.Errorf("сеанс %d: %+v", i, st)
		}
	}
}

// Много клиентов одновременно: у каждого свои переменные и порты, общая фабрика и один обработчик событий
func TestManyClientsInParallel(t *testing.T) {
	captureLog(t)

	factory, err := rtp.NewFactory("127.0.0.1", 24000, 25999)
	if err != nil {
		t.Fatal(err)
	}

	data := loadTestProto(t)

	parsed, err := NewParser(data.Methods).Parse(`
a = rtp_open(codecs = "PCMA")
b = rtp_open(codecs = "PCMA")
rtp_start(a, b.sdp, tone = 440)
rtp_start(b, a.sdp, tone = 1000)
sleep(0.4)
sa = rtp_stop(a)
sb = rtp_stop(b)
mine = INSTANCE
if sb.receivedPackets < 5 {
    fail("клиент", INSTANCE, "почти ничего не принял")
}
`)
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu      sync.Mutex
		mineSet = map[string]bool{}
	)

	results, _, err := load.Run(load.Options{Parallel: 25, Calls: 50}, func(instance int) load.CallResult {
		executor := NewExecutor(nil, data.Methods, "", data.Enums)
		executor.SetRTP(factory)
		executor.SetInstance(instance, true)
		executor.SetQuiet(true)

		err := executor.Execute(parsed)

		mine, _ := varValue(executor, "mine")

		mu.Lock()
		mineSet[mine] = true
		mu.Unlock()

		return load.CallResult{Err: err, RTP: executor.RTPStats()}
	})
	if err != nil {
		t.Fatal(err)
	}

	summary, failed := load.Summary(results, 0)
	if failed != 0 {
		t.Fatalf("клиенты с ошибками:\n%s", summary)
	}

	if len(mineSet) != 50 {
		t.Errorf("различных значений INSTANCE: %d, ожидалось 50", len(mineSet))
	}

	total := 0
	for _, r := range results {
		total += len(r.RTP)
	}

	if total != 100 {
		t.Errorf("учтено RTP-сеансов %d, ожидалось 100", total)
	}
}

func TestSourcesSummary(t *testing.T) {
	if got := sourcesSummary(rtp.Stats{Sources: 1}); got != "" {
		t.Errorf("один источник не должен попадать в строку: %q", got)
	}

	got := sourcesSummary(rtp.Stats{Sources: 3, OtherPayloadTypes: "101×1330"})
	if got != ", SSRC источников: 3, чужие payload type: 101×1330" {
		t.Errorf("%q", got)
	}
}
