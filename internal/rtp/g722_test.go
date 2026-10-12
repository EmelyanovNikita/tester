package rtp

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"testing"
)

// g722RefSignal — целочисленный сигнал 16 кГц, из которого ffmpeg получил эталоны в testdata (см. testdata/README.md).
// loud: громкий сигнал с насыщением и участками тишины.
func g722RefSignal(n int, loud bool) []int16 {
	out := make([]int16, n)
	lcg := 1

	for i := 0; i < n; i++ {
		a := (i*37%200 - 100) * 60

		t := i * 5 % 160
		tri := t
		if t >= 80 {
			tri = 160 - t
		}

		b := (tri - 40) * 100

		lcg = (lcg*1103515245 + 12345) & 0x7fffffff
		noise := (lcg>>16)%2001 - 1000
		e := 1 + (i/800)%8

		var v int

		switch {
		case !loud:
			v = (a + b + noise) * e >> 2
		case (i/2000)%4 == 3:
			v = 0
		default:
			v = (a + b + noise) * e >> 1
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
		}

		out[i] = int16(v)
	}

	return out
}

func pcmBytes(samples []int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}

	return b
}

var g722References = []struct {
	name       string
	file       string
	loud       bool
	decodedSum string // SHA-256 отсчётов (s16le), которые ffmpeg получил при декодировании эталона
}{
	{"обычный", "testdata/ref1.g722", false, "693154c6733dade7fd517dfa8db7d72722be7e2f5d3cc6accc70106e181e65c9"},
	{"громкий с тишиной", "testdata/ref2.g722", true, "a71f74e4b844dbdc403fbd74026895117656e96fba53a66e8fc54d0a00487be3"},
}

// Энкодер и декодер дают те же байты и отсчёты, что эталон ffmpeg
func TestG722MatchesReference(t *testing.T) {
	for _, ref := range g722References {
		want, err := os.ReadFile(ref.file)
		if err != nil {
			t.Fatal(err)
		}

		input := g722RefSignal(16000, ref.loud)

		encoded := newG722Encoder().Encode(input, nil)
		if len(encoded) != len(want) {
			t.Fatalf("%s: закодировано %d байт, ожидалось %d", ref.name, len(encoded), len(want))
		}

		for i := range want {
			if encoded[i] != want[i] {
				t.Fatalf("%s: байт %d: 0x%02X, эталон 0x%02X", ref.name, i, encoded[i], want[i])
			}
		}

		sum := sha256.Sum256(pcmBytes(newG722Decoder().Decode(want, nil)))
		if got := hex.EncodeToString(sum[:]); got != ref.decodedSum {
			t.Errorf("%s: декодированный сигнал не совпал с эталоном ffmpeg: %s", ref.name, got)
		}
	}
}

// Кодирование и декодирование по кадрам даёт то же, что целиком: состояние хранится между вызовами
func TestG722FramesEqualOneShot(t *testing.T) {
	input := g722RefSignal(16000, true)
	whole := newG722Encoder().Encode(input, nil)

	enc := newG722Encoder()

	var chunked []byte

	for i := 0; i < len(input); i += 320 { // кадр 20 мс
		chunked = append(chunked, enc.Encode(input[i:i+320], nil)...)
	}

	if string(chunked) != string(whole) {
		t.Fatal("кодирование по кадрам отличается от кодирования целиком")
	}

	dec := newG722Decoder()

	var pieces []int16

	for i := 0; i < len(whole); i += 160 {
		pieces = append(pieces, dec.Decode(whole[i:i+160], nil)...)
	}

	all := newG722Decoder().Decode(whole, nil)
	for i := range all {
		if pieces[i] != all[i] {
			t.Fatalf("декодирование по кадрам отличается на отсчёте %d", i)
		}
	}
}

// Тон 1 кГц после кодирования и декодирования остаётся чистым тоном
func TestG722ToneSurvives(t *testing.T) {
	const n = 16000

	tone := make([]int16, n)
	for i := range tone {
		tone[i] = int16(math.Round(10000 * math.Sin(2*math.Pi*1000*float64(i)/16000)))
	}

	decoded := newG722Decoder().Decode(newG722Encoder().Encode(tone, nil), nil)

	own := goertzel(decoded[1000:], 1000, 16000)
	other := goertzel(decoded[1000:], 3000, 16000)

	if own < other*1000 {
		t.Errorf("1 кГц: %v, 3 кГц: %v", own, other)
	}

	if len(decoded) != n {
		t.Errorf("отсчётов %d, ожидалось %d", len(decoded), n)
	}
}

func TestG722OddSampleDropped(t *testing.T) {
	if got := newG722Encoder().Encode(make([]int16, 5), nil); len(got) != 2 {
		t.Errorf("из 5 отсчётов ожидалось 2 байта, получили %d", len(got))
	}
}
