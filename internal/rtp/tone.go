package rtp

import "math"

// tone генерирует синусоиду: её хорошо слышно, и по частоте легко проверить, что аудио дошло без искажений
type tone struct {
	phase float64
	step  float64
	amp   float64
}

// toneAmplitude — примерно -10 dBFS: громко, но без клиппинга после кодирования
const toneAmplitude = 10000

// newTone создаёт генератор; частота 0 даёт тишину
func newTone(freqHz float64, sampleRate int) *tone {
	t := &tone{amp: toneAmplitude}

	if freqHz > 0 {
		t.step = 2 * math.Pi * freqHz / float64(sampleRate)
	} else {
		t.amp = 0
	}

	return t
}

// fill заполняет dst следующими отсчётами; фаза продолжается между вызовами
func (t *tone) fill(dst []int16) {
	for i := range dst {
		dst[i] = int16(math.Round(math.Sin(t.phase) * t.amp))

		t.phase += t.step
		if t.phase > 2*math.Pi {
			t.phase -= 2 * math.Pi
		}
	}
}
