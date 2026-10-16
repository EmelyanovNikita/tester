package rtp

import (
	"fmt"
	"sync"
)

// Звук из файла кодируется так же, как тон: один раз на сочетание «файл + кодек + ptime», а дальше все сеансы
// копируют готовые пакеты. Файл проигрывается по кругу, пока сеанс не остановят.

type fileKey struct {
	codec Codec
	ptime int
	path  string
}

var (
	fileMu    sync.Mutex
	fileCache = map[fileKey]*frameLoop{}
)

// fileLoop возвращает цикл из пакетов с записью из WAV-файла
func fileLoop(c Codec, ptimeMs int, path string) (*frameLoop, error) {
	key := fileKey{c, ptimeMs, path}

	fileMu.Lock()
	defer fileMu.Unlock()

	if loop, ok := fileCache[key]; ok {
		return loop, nil
	}

	audio, err := loadWAV(path)
	if err != nil {
		return nil, err
	}

	loop, err := buildFileLoop(c, ptimeMs, audio)
	if err != nil {
		return nil, fmt.Errorf("WAV %s: %w", path, err)
	}

	fileCache[key] = loop

	return loop, nil
}

func buildFileLoop(c Codec, ptimeMs int, audio wavAudio) (*frameLoop, error) {
	samples, err := c.FrameSamples(ptimeMs)
	if err != nil {
		return nil, err
	}

	size, err := c.PayloadBytes(ptimeMs)
	if err != nil {
		return nil, err
	}

	mono := resample(audio.samples, audio.rate, c.SampleRate)
	if len(mono) == 0 {
		return nil, fmt.Errorf("в файле нет звука")
	}

	count := (len(mono) + samples - 1) / samples // последний неполный кадр добивается тишиной
	enc := c.NewEncoder()
	pcm := make([]int16, samples*c.Channels)
	loop := &frameLoop{frames: make([][]byte, count)}

	for frame := 0; frame < count; frame++ {
		for i := 0; i < samples; i++ {
			v := int16(0)
			if at := frame*samples + i; at < len(mono) {
				v = mono[at]
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

	return loop, nil
}
