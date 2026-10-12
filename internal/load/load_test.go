package load

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tester_mm/internal/rtp"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name     string
		in       Options
		want     Options
		wantFail string
	}{
		{"по умолчанию", Options{}, Options{Parallel: 1, Calls: 1}, ""},
		{"calls по умолчанию равно parallel", Options{Parallel: 100}, Options{Parallel: 100, Calls: 100}, ""},
		{"parallel не больше calls", Options{Parallel: 50, Calls: 10}, Options{Parallel: 10, Calls: 10}, ""},
		{"много звонков, мало одновременных", Options{Parallel: 5, Calls: 100, Rate: 2.5}, Options{Parallel: 5, Calls: 100, Rate: 2.5}, ""},
		{"parallel отрицательный", Options{Parallel: -1}, Options{}, "parallel"},
		{"parallel слишком большой", Options{Parallel: maxParallel + 1}, Options{}, "parallel"},
		{"calls отрицательный", Options{Parallel: 1, Calls: -3}, Options{}, "calls"},
		{"rate отрицательный", Options{Parallel: 1, Rate: -1}, Options{}, "rate"},
	}

	for _, c := range cases {
		got, err := c.in.Normalize()

		if c.wantFail != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantFail) {
				t.Errorf("%s: ошибка %v, ожидалась с %q", c.name, err, c.wantFail)
			}

			continue
		}

		if err != nil || got != c.want {
			t.Errorf("%s: получили %+v, %v; ожидалось %+v", c.name, got, err, c.want)
		}
	}
}

func TestEnabled(t *testing.T) {
	if (Options{Parallel: 1, Calls: 1}).Enabled() || (Options{}).Enabled() {
		t.Error("один запуск не должен включать нагрузочный режим")
	}

	if !(Options{Parallel: 2}).Enabled() || !(Options{Parallel: 1, Calls: 5}).Enabled() {
		t.Error("несколько запусков должны включать нагрузочный режим")
	}
}

// Одновременно работает не больше Parallel клиентов, а всего запускается ровно Calls, каждый со своим номером
func TestRunLimitsConcurrency(t *testing.T) {
	var running, peak atomic.Int64

	var mu sync.Mutex

	seen := map[int]int{}

	results, _, err := Run(Options{Parallel: 4, Calls: 20}, func(instance int) CallResult {
		now := running.Add(1)

		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}

		time.Sleep(20 * time.Millisecond)
		running.Add(-1)

		mu.Lock()
		seen[instance]++
		mu.Unlock()

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 20 || len(seen) != 20 {
		t.Fatalf("результатов %d, различных клиентов %d, ожидалось 20", len(results), len(seen))
	}

	for n := 1; n <= 20; n++ {
		if seen[n] != 1 || results[n-1].Instance != n {
			t.Errorf("клиент %d: запусков %d, номер в результате %d", n, seen[n], results[n-1].Instance)
		}
	}

	if peak.Load() != 4 {
		t.Errorf("одновременно работало до %d клиентов, ожидалось ровно 4", peak.Load())
	}
}

// Все клиенты стартуют сразу, если Parallel == Calls и Rate не задан
func TestRunAllAtOnce(t *testing.T) {
	var running, peak atomic.Int64

	_, _, err := Run(Options{Parallel: 30}, func(int) CallResult {
		now := running.Add(1)

		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}

		time.Sleep(100 * time.Millisecond)
		running.Add(-1)

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if peak.Load() != 30 {
		t.Errorf("одновременно работало %d клиентов, ожидалось 30", peak.Load())
	}
}

// Rate ограничивает частоту запусков: 6 запусков по 20 в секунду занимают не меньше 5 интервалов по 50 мс
func TestRunRate(t *testing.T) {
	var mu sync.Mutex

	var starts []time.Time

	begin := time.Now()

	_, elapsed, err := Run(Options{Parallel: 6, Rate: 20}, func(int) CallResult {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(starts) != 6 {
		t.Fatalf("запусков %d", len(starts))
	}

	last := starts[0]
	for _, s := range starts {
		if s.After(last) {
			last = s
		}
	}

	if span := last.Sub(begin); span < 240*time.Millisecond {
		t.Errorf("шесть запусков уложились в %v, при 20/с нужно не меньше 250 мс", span)
	}

	if elapsed < 240*time.Millisecond {
		t.Errorf("общее время %v", elapsed)
	}
}

// Если клиенты не успевают за Rate (мало мест), запуски не выстраиваются в пачку
func TestRunRateWithBusySlots(t *testing.T) {
	var mu sync.Mutex

	var starts []time.Time

	_, _, err := Run(Options{Parallel: 1, Calls: 3, Rate: 1000}, func(int) CallResult {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if gap := starts[2].Sub(starts[0]); gap < 55*time.Millisecond {
		t.Errorf("при одном месте третий клиент стартовал через %v, ожидалось >= 60 мс", gap)
	}
}

func TestRunPanicBecomesError(t *testing.T) {
	results, _, err := Run(Options{Parallel: 3}, func(instance int) CallResult {
		if instance == 2 {
			panic("авария")
		}

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), "авария") {
		t.Errorf("паника должна стать ошибкой клиента: %v", results[1].Err)
	}

	if results[0].Err != nil || results[2].Err != nil {
		t.Error("остальные клиенты не должны пострадать")
	}
}

func TestRunMeasuresDuration(t *testing.T) {
	results, _, _ := Run(Options{Parallel: 1}, func(int) CallResult {
		time.Sleep(50 * time.Millisecond)

		return CallResult{}
	})

	if results[0].Duration < 50*time.Millisecond {
		t.Errorf("длительность %v", results[0].Duration)
	}
}

func TestRunRejectsBadOptions(t *testing.T) {
	if _, _, err := Run(Options{Parallel: -5}, func(int) CallResult { return CallResult{} }); err == nil {
		t.Error("ожидалась ошибка")
	}
}

func TestSummary(t *testing.T) {
	results := []CallResult{
		{Instance: 1, Duration: 1 * time.Second, RTP: []rtp.Stats{
			{SentPackets: 100, ReceivedPackets: 90, LostPackets: 10, LossPercent: 10, JitterMs: 2, DTMFSent: "12"},
			{SentPackets: 100, ReceivedPackets: 100, JitterMs: 4, DTMFReceived: "12"},
		}},
		{Instance: 2, Duration: 3 * time.Second, Err: errors.New("команда 3: MM вернул reply_code=RC_INTERNAL_ERROR"),
			RTP: []rtp.Stats{{SentPackets: 50}}},
		{Instance: 3, Duration: 2 * time.Second, Err: errors.New("команда 3: MM вернул reply_code=RC_INTERNAL_ERROR")},
		{Instance: 4, Duration: 2 * time.Second, Err: errors.New("fail: потери")},
	}

	text, failed := Summary(results, 4*time.Second)

	if failed != 3 {
		t.Errorf("failed = %d, ожидалось 3", failed)
	}

	for _, want := range []string{
		"клиентов 4, успешно 1, с ошибками 3 (25.0% успешных)",
		"мин 1.0 с, среднее 2.0 с, 95% 3.0 с, макс 3.0 с",
		"сеансов 3, отправлено пакетов 250, принято 190, потеряно 10 (5.000%, худший сеанс 10.000%)",
		"среднее 2.000 мс, макс 4.000 мс; сеансов без входящего потока: 1",
		"DTMF: цифр отправлено 2, принято 2",
		"2 × команда 3: MM вернул reply_code=RC_INTERNAL_ERROR",
		"1 × fail: потери",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q:\n%s", want, text)
		}
	}
}

func TestSummaryWithoutRTPAndErrors(t *testing.T) {
	text, failed := Summary([]CallResult{{Instance: 1, Duration: time.Second}}, time.Second)

	if failed != 0 || strings.Contains(text, "RTP:") || strings.Contains(text, "Ошибки") {
		t.Errorf("failed = %d, сводка:\n%s", failed, text)
	}
}

func TestSummaryEmpty(t *testing.T) {
	if text, failed := Summary(nil, time.Second); failed != 0 || !strings.Contains(text, "ни одного") {
		t.Errorf("%q, %d", text, failed)
	}
}

func TestSummaryLimitsErrorKinds(t *testing.T) {
	var results []CallResult

	for i := 0; i < 8; i++ {
		results = append(results, CallResult{Instance: i + 1, Err: errors.New(strings.Repeat("x", i+1))})
	}

	text, _ := Summary(results, time.Second)

	if !strings.Contains(text, "и ещё 3 видов ошибок") {
		t.Errorf("ожидалось сокращение списка ошибок:\n%s", text)
	}
}

func TestShortenLongError(t *testing.T) {
	long := strings.Repeat("я", 1000)

	if got := shorten(long); len([]rune(got)) != maxErrorLen+1 {
		t.Errorf("длина %d", len([]rune(got)))
	}

	if got := shorten("a\n  b\t c"); got != "a b c" {
		t.Errorf("%q", got)
	}
}

func TestPercentile(t *testing.T) {
	d := func(ms ...int) []time.Duration {
		var out []time.Duration
		for _, m := range ms {
			out = append(out, time.Duration(m)*time.Millisecond)
		}

		return out
	}

	if got := percentile(d(10), 95); got != 10*time.Millisecond {
		t.Errorf("%v", got)
	}

	list := d(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20)
	if got := percentile(list, 95); got != 19*time.Millisecond {
		t.Errorf("p95 из 20 = %v, ожидалось 19 мс", got)
	}

	if got := percentile(list, 100); got != 20*time.Millisecond {
		t.Errorf("p100 = %v", got)
	}
}
