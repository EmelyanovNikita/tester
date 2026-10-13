package load

import (
	"errors"
	"sort"
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
		{"по умолчанию", Options{}, Options{Rate: DefaultRate}, ""},
		{"заданные значения", Options{Rate: 2.5, Limit: 5, Calls: 100}, Options{Rate: 2.5, Limit: 5, Calls: 100}, ""},
		{"limit не больше calls", Options{Limit: 50, Calls: 10}, Options{Rate: DefaultRate, Limit: 10, Calls: 10}, ""},
		{"rate отрицательный", Options{Rate: -1}, Options{}, "rate"},
		{"rate слишком большой", Options{Rate: maxRate + 1}, Options{}, "rate"},
		{"limit отрицательный", Options{Limit: -1}, Options{}, "limit"},
		{"calls отрицательный", Options{Calls: -3}, Options{}, "calls"},
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
	if (Options{}).Enabled() {
		t.Error("без параметров нагрузочный режим не нужен")
	}

	for _, opts := range []Options{{Rate: 1}, {Limit: 1}, {Calls: 1}} {
		if !opts.Enabled() {
			t.Errorf("%+v должен включать нагрузочный режим", opts)
		}
	}
}

// peakTracker считает, сколько клиентов работает одновременно
type peakTracker struct {
	running, peak atomic.Int64
}

func (p *peakTracker) enter() {
	now := p.running.Add(1)

	for {
		old := p.peak.Load()
		if now <= old || p.peak.CompareAndSwap(old, now) {
			return
		}
	}
}

func (p *peakTracker) leave() { p.running.Add(-1) }

// Одновременно работает не больше Limit клиентов, а всего запускается ровно Calls, каждый со своим номером
func TestRunLimitsConcurrency(t *testing.T) {
	var peak peakTracker

	var mu sync.Mutex

	seen := map[int]int{}

	results, _, err := Run(Options{Rate: 1000, Limit: 4, Calls: 20}, nil, func(instance int) CallResult {
		peak.enter()
		time.Sleep(20 * time.Millisecond)
		peak.leave()

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

	if peak.peak.Load() != 4 {
		t.Errorf("одновременно работало до %d клиентов, ожидалось ровно 4", peak.peak.Load())
	}
}

// Клиенты стартуют не пачкой, а равномерно: интервалы между стартами близки к 1/Rate
func TestRunStartsEvenly(t *testing.T) {
	var mu sync.Mutex

	var starts []time.Time

	_, _, err := Run(Options{Rate: 50, Calls: 10}, nil, func(int) CallResult {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(starts) != 10 {
		t.Fatalf("запусков %d", len(starts))
	}

	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	// 9 интервалов по 20 мс; допуск на планировщик
	if span := starts[9].Sub(starts[0]); span < 160*time.Millisecond {
		t.Errorf("10 стартов при 50/с заняли %v, ожидалось около 180 мс", span)
	}

	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 10*time.Millisecond {
			t.Errorf("старты %d и %d разделяет всего %v: клиенты стартуют пачкой", i, i+1, gap)
		}
	}
}

// Когда все места заняты, расписание сдвигается и накопившиеся старты не уходят пачкой после освобождения мест
func TestRunNoBurstAfterBlocked(t *testing.T) {
	var mu sync.Mutex

	var starts []time.Time

	_, _, err := Run(Options{Rate: 100, Limit: 2, Calls: 8}, nil, func(instance int) CallResult {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()

		if instance <= 2 {
			time.Sleep(100 * time.Millisecond) // первые двое долго занимают оба места
		}

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	// после освобождения мест (≈100 мс) остальные шесть стартуют по 10 мс, а не одновременно
	for i := 3; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 5*time.Millisecond {
			t.Errorf("старты %d и %d разделяет %v: накопленные запуски ушли пачкой", i, i+1, gap)
		}
	}
}

// Закрытие stop прекращает запуск новых клиентов, уже запущенные дорабатывают; Calls = 0 — без ограничения числа
func TestRunStopsOnSignal(t *testing.T) {
	stop := make(chan struct{})

	var launched atomic.Int64

	results, _, err := Run(Options{Rate: 200}, stop, func(int) CallResult {
		if launched.Add(1) == 5 {
			close(stop)
		}

		time.Sleep(20 * time.Millisecond)

		return CallResult{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) < 5 || len(results) > 8 {
		t.Errorf("запущено %d клиентов, ожидалось около пяти", len(results))
	}

	for i, r := range results {
		if r.Instance != i+1 || r.Duration < 20*time.Millisecond {
			t.Errorf("клиент %d не дождались: %+v", i+1, r)
		}
	}
}

func TestRunPanicBecomesError(t *testing.T) {
	results, _, err := Run(Options{Rate: 1000, Calls: 3}, nil, func(instance int) CallResult {
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
	results, _, _ := Run(Options{Calls: 1}, nil, func(int) CallResult {
		time.Sleep(50 * time.Millisecond)

		return CallResult{}
	})

	if results[0].Duration < 50*time.Millisecond {
		t.Errorf("длительность %v", results[0].Duration)
	}
}

func TestRunRejectsBadOptions(t *testing.T) {
	if _, _, err := Run(Options{Limit: -5}, nil, func(int) CallResult { return CallResult{} }); err == nil {
		t.Error("ожидалась ошибка")
	}
}

func TestSummary(t *testing.T) {
	results := []CallResult{
		{Instance: 1, Duration: 1 * time.Second, RTP: []rtp.Stats{
			{SentPackets: 100, ReceivedPackets: 90, LostPackets: 10, LossPercent: 10, JitterMs: 2, DTMFSent: "12"},
			{SentPackets: 100, ReceivedPackets: 100, JitterMs: 4, DTMFReceived: "12", Sources: 2},
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
		"больше одного SSRC (чужой или перезапущенный поток): 1",
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
