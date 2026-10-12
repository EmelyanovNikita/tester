package rtp

import (
	"fmt"
	"math"
	"sync"
)

// Тон кодируется один раз: цикл из целого числа пакетов, который при повторе даёт непрерывный звук.
// Цикл общий для всех сеансов с тем же кодеком, ptime и частотой, поэтому отправка сводится к копированию готовых байтов
// и нагрузка на процессор от числа потоков почти не зависит от кодека.

// toneAmplitude — примерно -10 dBFS: громко, но без клиппинга после кодирования
const toneAmplitude = 10000

// frameLoop — закодированные пакеты цикла по порядку
type frameLoop struct {
	frames [][]byte
}

type loopKey struct {
	codec  Codec
	ptime  int
	freqHz int
}

var (
	loopMu    sync.Mutex
	loopCache = map[loopKey]*frameLoop{}
)

// toneLoop возвращает цикл тона для кодека; freqHz = 0 — тишина. Частота целая: так цикл замыкается без щелчка.
func toneLoop(c Codec, ptimeMs, freqHz int) (*frameLoop, error) {
	if freqHz < 0 {
		return nil, fmt.Errorf("частота тона не может быть отрицательной")
	}

	key := loopKey{c, ptimeMs, freqHz}

	loopMu.Lock()
	defer loopMu.Unlock()

	if loop, ok := loopCache[key]; ok {
		return loop, nil
	}

	loop, err := buildLoop(c, ptimeMs, freqHz)
	if err != nil {
		return nil, err
	}

	loopCache[key] = loop

	return loop, nil
}

func buildLoop(c Codec, ptimeMs, freqHz int) (*frameLoop, error) {
	samples, err := c.FrameSamples(ptimeMs)
	if err != nil {
		return nil, err
	}

	size, err := c.PayloadBytes(ptimeMs)
	if err != nil {
		return nil, err
	}

	// цикл длится целое число секунд: тогда целое число периодов тона укладывается в него точно
	count := c.SampleRate / gcd(c.SampleRate, samples)

	enc := c.NewEncoder()
	pcm := make([]int16, samples*c.Channels)
	loop := &frameLoop{frames: make([][]byte, count)}

	// Кодеки с памятью (G.722) выходят на установившийся режим не с первого прохода: проходим цикл несколько раз,
	// а в запас берём последний проход, чтобы стык цикла был как можно ближе к бесшовному
	passes := 1
	if c.kind == kindG722 {
		passes = 4
	}

	for pass := 0; pass < passes; pass++ {
		for frame := 0; frame < count; frame++ {
			for i := 0; i < samples; i++ {
				v := int16(0)
				if freqHz > 0 {
					phase := 2 * math.Pi * float64(freqHz) * float64(frame*samples+i) / float64(c.SampleRate)
					v = int16(math.Round(toneAmplitude * math.Sin(phase)))
				}

				for ch := 0; ch < c.Channels; ch++ {
					pcm[i*c.Channels+ch] = v
				}
			}

			out := enc.Encode(pcm, make([]byte, 0, size))
			if len(out) != size {
				return nil, fmt.Errorf("%s: кодер выдал %d байт вместо %d", c, len(out), size)
			}

			loop.frames[frame] = out
		}
	}

	return loop, nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}

	return a
}
