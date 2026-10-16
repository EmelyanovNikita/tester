package rtp

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	wavFormatPCM        = 1
	wavFormatExtensible = 0xFFFE
	maxWAVBytes         = 64 << 20 // защита от случайно указанного огромного файла
)

// wavAudio — звук из WAV-файла: моно, 16 бит
type wavAudio struct {
	samples []int16
	rate    int
}

// loadWAV читает WAV: PCM, 16 бит, любая частота, моно или стерео (стерео сводится к моно усреднением каналов)
func loadWAV(path string) (wavAudio, error) {
	info, err := os.Stat(path)
	if err != nil {
		return wavAudio{}, fmt.Errorf("WAV: %w", err)
	}

	if info.Size() > maxWAVBytes {
		return wavAudio{}, fmt.Errorf("WAV %s: файл больше %d МБ", path, maxWAVBytes>>20)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return wavAudio{}, fmt.Errorf("WAV: %w", err)
	}

	audio, err := parseWAV(data)
	if err != nil {
		return wavAudio{}, fmt.Errorf("WAV %s: %w", path, err)
	}

	return audio, nil
}

func parseWAV(data []byte) (wavAudio, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return wavAudio{}, fmt.Errorf("это не WAV (нет заголовка RIFF/WAVE)")
	}

	var (
		format, channels, bits, rate int
		pcm                          []byte
		haveFmt                      bool
	)

	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8

		end := body + size
		if end > len(data) || end < body {
			end = len(data) // обрезанный файл или неверный размер (поток): берём, что есть
		}

		switch id {
		case "fmt ":
			if end-body < 16 {
				return wavAudio{}, fmt.Errorf("слишком короткий блок fmt")
			}

			format = int(binary.LittleEndian.Uint16(data[body:]))
			channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			rate = int(binary.LittleEndian.Uint32(data[body+4:]))
			bits = int(binary.LittleEndian.Uint16(data[body+14:]))
			haveFmt = true
		case "data":
			pcm = data[body:end]
		}

		pos = end + size%2 // блоки выровнены по чётному байту
	}

	if !haveFmt || pcm == nil {
		return wavAudio{}, fmt.Errorf("нет блока fmt или data")
	}

	if (format != wavFormatPCM && format != wavFormatExtensible) || bits != 16 {
		return wavAudio{}, fmt.Errorf("нужен PCM 16 бит (получено: формат %d, %d бит); конвертация: ffmpeg -i in.wav -ar 8000 -ac 1 -c:a pcm_s16le out.wav", format, bits)
	}

	if channels < 1 || channels > 2 || rate <= 0 {
		return wavAudio{}, fmt.Errorf("поддерживаются моно и стерео, получено каналов %d, частота %d", channels, rate)
	}

	frames := len(pcm) / (2 * channels)
	samples := make([]int16, frames)

	for i := range samples {
		sum := 0

		for ch := 0; ch < channels; ch++ {
			sum += int(int16(binary.LittleEndian.Uint16(pcm[(i*channels+ch)*2:])))
		}

		samples[i] = int16(sum / channels)
	}

	return wavAudio{samples: samples, rate: rate}, nil
}

// resample приводит звук к нужной частоте линейной интерполяцией. Для речи в телефонном канале этого достаточно.
func resample(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return in
	}

	out := make([]int16, int(int64(len(in))*int64(to)/int64(from)))

	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		idx := int(pos)
		frac := pos - float64(idx)

		next := idx + 1
		if next >= len(in) {
			next = len(in) - 1
		}

		out[i] = int16(float64(in[idx])*(1-frac) + float64(in[next])*frac)
	}

	return out
}
