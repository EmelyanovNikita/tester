// Package load — параллельный запуск одного сценария многими клиентами (нагрузочный режим) и сводка по результатам.
//
// Клиент — одно выполнение сценария («звонок»). Одновременно выполняется не больше Parallel клиентов, всего их Calls,
// запускаются они не чаще Rate раз в секунду.
package load

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tester_mm/internal/rtp"
)

const (
	// maxParallel — защита от опечатки вроде --parallel 100000: каждому клиенту нужны горутины и порты
	maxParallel = 10000

	// progressEvery — как часто выводится строка о ходе теста
	progressEvery = 5 * time.Second

	// maxShownErrors — сколько разных ошибок показывать в сводке
	maxShownErrors = 5

	// maxErrorLen — длина текста ошибки в сводке
	maxErrorLen = 300
)

// Options — параметры нагрузки
type Options struct {
	Parallel int     // сколько клиентов выполняется одновременно
	Calls    int     // сколько всего клиентов (запусков сценария); 0 — столько же, сколько Parallel
	Rate     float64 // запусков в секунду; 0 — без ограничения
}

// Enabled сообщает, что нужен нагрузочный режим (больше одного запуска)
func (opts Options) Enabled() bool {
	return opts.Parallel > 1 || opts.Calls > 1
}

// Normalize проверяет параметры и подставляет значения по умолчанию
func (opts Options) Normalize() (Options, error) {
	if opts.Parallel == 0 {
		opts.Parallel = 1
	}

	if opts.Parallel < 1 || opts.Parallel > maxParallel {
		return opts, fmt.Errorf("parallel: ожидается число от 1 до %d, получено %d", maxParallel, opts.Parallel)
	}

	if opts.Calls < 0 {
		return opts, fmt.Errorf("calls: ожидается неотрицательное число, получено %d", opts.Calls)
	}

	if opts.Calls == 0 {
		opts.Calls = opts.Parallel
	}

	if opts.Parallel > opts.Calls {
		opts.Parallel = opts.Calls
	}

	if opts.Rate < 0 {
		return opts, fmt.Errorf("rate: ожидается неотрицательное число запусков в секунду, получено %v", opts.Rate)
	}

	return opts, nil
}

// CallResult — итог одного клиента
type CallResult struct {
	Instance int // номер клиента с 1 (значение переменной INSTANCE)
	Err      error
	Duration time.Duration
	RTP      []rtp.Stats // итоговые счётчики RTP-сеансов этого клиента
}

// Run выполняет run для клиентов 1..Calls и возвращает результаты по номерам клиентов и общее время.
// Паника в run превращается в ошибку клиента, остальные клиенты продолжают работу.
func Run(opts Options, run func(instance int) CallResult) ([]CallResult, time.Duration, error) {
	opts, err := opts.Normalize()
	if err != nil {
		return nil, 0, err
	}

	var (
		started, finished, failedCount atomic.Int64
		wg                             sync.WaitGroup
	)

	results := make([]CallResult, opts.Calls)
	slots := make(chan struct{}, opts.Parallel)
	begin := time.Now()

	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(progressEvery)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s, f := started.Load(), finished.Load()
				log.Printf("Нагрузка: запущено %d из %d, выполняется %d, завершено %d (с ошибками %d), прошло %.0f с",
					s, opts.Calls, s-f, f, failedCount.Load(), time.Since(begin).Seconds())
			case <-done:
				return
			}
		}
	}()

	var interval time.Duration
	if opts.Rate > 0 {
		interval = time.Duration(float64(time.Second) / opts.Rate)
	}

	nextStart := begin

	for n := 1; n <= opts.Calls; n++ {
		slots <- struct{}{} // ждём свободное место

		if wait := time.Until(nextStart); wait > 0 {
			time.Sleep(wait)
		}

		nextStart = time.Now().Add(interval)

		started.Add(1)
		wg.Add(1)

		go func(instance int) {
			defer wg.Done()
			defer func() { <-slots }()

			result := runSafely(run, instance)
			result.Instance = instance
			results[instance-1] = result

			finished.Add(1)

			if result.Err != nil {
				failedCount.Add(1)
			}
		}(n)
	}

	wg.Wait()

	return results, time.Since(begin), nil
}

func runSafely(run func(instance int) CallResult, instance int) (result CallResult) {
	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			result.Err = fmt.Errorf("паника в клиенте: %v", r)
		}

		result.Duration = time.Since(start)
	}()

	return run(instance)
}

// Summary формирует сводку и возвращает число клиентов с ошибками
func Summary(results []CallResult, elapsed time.Duration) (string, int) {
	var text strings.Builder

	failed := 0
	errorCounts := map[string]int{}
	durations := make([]time.Duration, 0, len(results))

	var stats []rtp.Stats

	for _, result := range results {
		durations = append(durations, result.Duration)
		stats = append(stats, result.RTP...)

		if result.Err != nil {
			failed++
			errorCounts[shorten(result.Err.Error())]++
		}
	}

	total := len(results)
	if total == 0 {
		return "Итог нагрузки: ни одного клиента не запускалось", 0
	}

	fmt.Fprintf(&text, "Итог нагрузки: клиентов %d, успешно %d, с ошибками %d (%.1f%% успешных), всего %.1f с, %.2f запусков/с\n",
		total, total-failed, failed, float64(total-failed)/float64(total)*100, elapsed.Seconds(), float64(total)/elapsed.Seconds())

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	fmt.Fprintf(&text, "  Длительность клиента: мин %.1f с, среднее %.1f с, 95%% %.1f с, макс %.1f с\n",
		durations[0].Seconds(), average(durations).Seconds(), percentile(durations, 95).Seconds(), durations[len(durations)-1].Seconds())

	if len(stats) > 0 {
		text.WriteString(rtpSummary(stats))
	}

	if failed > 0 {
		text.WriteString(errorsSummary(errorCounts))
	}

	return strings.TrimRight(text.String(), "\n"), failed
}

func rtpSummary(stats []rtp.Stats) string {
	var (
		sent, received, lost, otherPayload uint64
		jitterSum, jitterMax               float64
		silent, multiSource                int
		dtmfSent, dtmfReceived             int
		worstLoss                          float64
	)

	for _, st := range stats {
		sent += st.SentPackets
		received += st.ReceivedPackets
		lost += st.LostPackets
		otherPayload += st.OtherPayloadPackets
		jitterSum += st.JitterMs

		if st.JitterMs > jitterMax {
			jitterMax = st.JitterMs
		}

		if st.LossPercent > worstLoss {
			worstLoss = st.LossPercent
		}

		if st.ReceivedPackets == 0 {
			silent++
		}

		if st.Sources > 1 {
			multiSource++
		}

		dtmfSent += len(st.DTMFSent)
		dtmfReceived += len(st.DTMFReceived)
	}

	lossPercent := 0.0
	if received+lost > 0 {
		lossPercent = float64(lost) / float64(received+lost) * 100
	}

	var text strings.Builder

	fmt.Fprintf(&text, "  RTP: сеансов %d, отправлено пакетов %d, принято %d, потеряно %d (%.3f%%, худший сеанс %.3f%%)\n",
		len(stats), sent, received, lost, lossPercent, worstLoss)
	fmt.Fprintf(&text, "       джиттер: среднее %.3f мс, макс %.3f мс; сеансов без входящего потока: %d\n",
		jitterSum/float64(len(stats)), jitterMax, silent)

	if dtmfSent+dtmfReceived > 0 {
		fmt.Fprintf(&text, "       DTMF: цифр отправлено %d, принято %d\n", dtmfSent, dtmfReceived)
	}

	if multiSource > 0 {
		fmt.Fprintf(&text, "       сеансов, на которые писало больше одного SSRC (чужой или перезапущенный поток): %d\n", multiSource)
	}

	if otherPayload > 0 {
		fmt.Fprintf(&text, "       пакетов с незнакомым payload type: %d\n", otherPayload)
	}

	return text.String()
}

func errorsSummary(counts map[string]int) string {
	type entry struct {
		text  string
		count int
	}

	entries := make([]entry, 0, len(counts))
	for text, count := range counts {
		entries = append(entries, entry{text, count})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}

		return entries[i].text < entries[j].text
	})

	var text strings.Builder

	text.WriteString("  Ошибки (по числу клиентов):\n")

	for i, e := range entries {
		if i == maxShownErrors {
			fmt.Fprintf(&text, "    ... и ещё %d видов ошибок\n", len(entries)-maxShownErrors)
			break
		}

		fmt.Fprintf(&text, "    %d × %s\n", e.count, e.text)
	}

	return text.String()
}

func shorten(text string) string {
	text = strings.Join(strings.Fields(text), " ")

	runes := []rune(text)
	if len(runes) > maxErrorLen {
		return string(runes[:maxErrorLen]) + "…"
	}

	return text
}

func average(sorted []time.Duration) time.Duration {
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}

	return sum / time.Duration(len(sorted))
}

// percentile возвращает значение p-го процентиля отсортированного списка (метод «ближайшего ранга»)
func percentile(sorted []time.Duration, p int) time.Duration {
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}

	return sorted[rank-1]
}
