package rtp

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeTestWAV создаёт WAV с синусом заданной частоты и длительности
func writeTestWAV(t *testing.T, rate, channels int, seconds float64, freq float64) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "in.wav")

	w, err := createWAV(path, rate, channels)
	if err != nil {
		t.Fatal(err)
	}

	n := int(float64(rate) * seconds)
	buf := make([]int16, 0, n*channels)

	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
		for ch := 0; ch < channels; ch++ {
			buf = append(buf, v)
		}
	}

	if err := w.write(buf); err != nil {
		t.Fatal(err)
	}

	if err := w.close(); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadWAVRoundTrip(t *testing.T) {
	audio, err := loadWAV(writeTestWAV(t, 16000, 1, 0.5, 440))
	if err != nil {
		t.Fatal(err)
	}

	if audio.rate != 16000 || len(audio.samples) != 8000 {
		t.Fatalf("частота %d, отсчётов %d; ждали 16000 и 8000", audio.rate, len(audio.samples))
	}
}

func TestLoadWAVStereoDownmix(t *testing.T) {
	audio, err := loadWAV(writeTestWAV(t, 8000, 2, 0.25, 300))
	if err != nil {
		t.Fatal(err)
	}

	if len(audio.samples) != 2000 {
		t.Fatalf("отсчётов %d, ждали 2000 (стерео сводится к моно)", len(audio.samples))
	}
}

func TestLoadWAVErrors(t *testing.T) {
	if _, err := loadWAV(filepath.Join(t.TempDir(), "нет.wav")); err == nil {
		t.Error("ждали ошибку для несуществующего файла")
	}

	bad := filepath.Join(t.TempDir(), "bad.wav")
	if err := os.WriteFile(bad, []byte("это не звук, а просто текст"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadWAV(bad); err == nil {
		t.Error("ждали ошибку для файла без RIFF")
	}
}

func TestResampleLength(t *testing.T) {
	in := make([]int16, 44100)
	if out := resample(in, 44100, 8000); len(out) != 8000 {
		t.Fatalf("длина %d, ждали 8000", len(out))
	}
}

// Файл в 0,5 с при ptime 20 даёт 25 пакетов; после декодирования слышна та же частота, что в файле.
func TestFileLoopFramesAndContent(t *testing.T) {
	path := writeTestWAV(t, 8000, 1, 0.5, 1000)

	loop, err := fileLoop(PCMU, 20, path)
	if err != nil {
		t.Fatal(err)
	}

	if len(loop.frames) != 25 {
		t.Fatalf("пакетов %d, ждали 25", len(loop.frames))
	}

	var decoded []int16
	dec := PCMU.NewDecoder()

	for _, f := range loop.frames {
		decoded = append(decoded, dec.Decode(f, nil)...)
	}

	// число переходов через ноль: 1000 Гц за 0,5 с — около 1000
	crossings := 0

	for i := 1; i < len(decoded); i++ {
		if (decoded[i-1] < 0) != (decoded[i] < 0) {
			crossings++
		}
	}

	if crossings < 950 || crossings > 1050 {
		t.Fatalf("переходов через ноль %d, ждали около 1000 (тон 1000 Гц)", crossings)
	}

	again, err := fileLoop(PCMU, 20, path)
	if err != nil || again != loop {
		t.Fatal("повторный запрос должен вернуть цикл из кэша")
	}
}

func TestFileLoopResamplesForG722(t *testing.T) {
	path := writeTestWAV(t, 8000, 1, 0.5, 500)

	loop, err := fileLoop(G722, 20, path)
	if err != nil {
		t.Fatal(err)
	}

	// 0,5 с при 16 кГц и ptime 20 — те же 25 пакетов
	if len(loop.frames) != 25 {
		t.Fatalf("пакетов %d, ждали 25", len(loop.frames))
	}
}
