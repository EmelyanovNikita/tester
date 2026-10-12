package rtp

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestG711Anchors(t *testing.T) {
	// Значения из таблиц G.711
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"ulaw 0", int(encodeULaw(0)), 0xFF},
		{"ulaw 1000", int(encodeULaw(1000)), 0xCE},
		{"ulaw decode 0xFF", int(decodeULaw(0xFF)), 0},
		{"ulaw decode 0x00", int(decodeULaw(0x00)), -32124},
		{"ulaw decode 0x80", int(decodeULaw(0x80)), 32124},
		{"ulaw clip", int(encodeULaw(32767)), 0x80},
		{"alaw 0", int(encodeALaw(0)), 0xD5},
		{"alaw 1000", int(encodeALaw(1000)), 0xFA},
		{"alaw decode 0xD5", int(decodeALaw(0xD5)), 8},
		{"alaw decode 0x55", int(decodeALaw(0x55)), -8},
		{"alaw decode 0xFA", int(decodeALaw(0xFA)), 1008},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: получили %d (0x%X), ожидалось %d (0x%X)", c.name, c.got, c.got, c.want, c.want)
		}
	}
}

type law8 struct {
	name   string
	encode func(int16) byte
	decode func(byte) int16
}

var g711Laws = []law8{
	{"PCMU", encodeULaw, decodeULaw},
	{"PCMA", encodeALaw, decodeALaw},
}

func TestG711RoundTrip(t *testing.T) {
	for _, codec := range g711Laws {
		var prev int

		for x := -32768; x <= 32767; x++ {
			got := int(codec.decode(codec.encode(int16(x))))

			// погрешность квантования: не более 1/16 от значения плюс запас на малые амплитуды
			limit := int(math.Abs(float64(x)))/16 + 16
			if x >= 32635 || x <= -32635 {
				limit = 700 // у верхней границы сказывается ограничение амплитуды
			}

			if diff := got - x; diff > limit || -diff > limit {
				t.Fatalf("%s: %d → %d, погрешность больше %d", codec.name, x, got, limit)
			}

			if x > -32768 && got < prev {
				t.Fatalf("%s: не монотонно на %d: %d после %d", codec.name, x, got, prev)
			}

			prev = got
		}
	}
}

func TestG711EveryCodeIsStable(t *testing.T) {
	for _, codec := range g711Laws {
		for c := 0; c < 256; c++ {
			code := byte(c)

			// «отрицательный ноль» μ-law (0x7F) кодируется обратно как обычный ноль
			if codec.name == "PCMU" && code == 0x7F {
				continue
			}

			if back := codec.encode(codec.decode(code)); back != code {
				t.Errorf("%s: код 0x%02X → %d → 0x%02X", codec.name, code, codec.decode(code), back)
			}
		}
	}
}

func TestParsePacket(t *testing.T) {
	packet := make([]byte, headerLen+3)
	Header{Marker: true, PayloadType: 8, Seq: 65535, Timestamp: 0xDEADBEEF, SSRC: 42}.put(packet)
	copy(packet[headerLen:], []byte{1, 2, 3})

	header, payload, err := ParsePacket(packet)
	if err != nil {
		t.Fatal(err)
	}

	if !header.Marker || header.PayloadType != 8 || header.Seq != 65535 || header.Timestamp != 0xDEADBEEF || header.SSRC != 42 {
		t.Errorf("заголовок: %+v", header)
	}

	if len(payload) != 3 || payload[0] != 1 || payload[2] != 3 {
		t.Errorf("нагрузка: %v", payload)
	}
}

func TestParsePacketCSRCExtensionPadding(t *testing.T) {
	// 2 CSRC + расширение из одного слова + padding 2 байта
	packet := make([]byte, headerLen+8+4+4+3+2)
	Header{PayloadType: 0}.put(packet)
	packet[0] |= 0x02 | 0x10 | 0x20
	binary.BigEndian.PutUint16(packet[headerLen+8+2:], 1)
	copy(packet[headerLen+8+4+4:], []byte{7, 8, 9})
	packet[len(packet)-1] = 2

	_, payload, err := ParsePacket(packet)
	if err != nil {
		t.Fatal(err)
	}

	if len(payload) != 3 || payload[0] != 7 || payload[2] != 9 {
		t.Errorf("нагрузка: %v", payload)
	}
}

func TestParsePacketErrors(t *testing.T) {
	valid := make([]byte, headerLen)
	Header{}.put(valid)

	badVersion := append([]byte{}, valid...)
	badVersion[0] = 1 << 6

	truncatedCSRC := append([]byte{}, valid...)
	truncatedCSRC[0] |= 0x03

	truncatedExt := append([]byte{}, valid...)
	truncatedExt[0] |= 0x10

	badPadding := append(append([]byte{}, valid...), 9)
	badPadding[0] |= 0x20

	cases := map[string][]byte{
		"короткий":       valid[:5],
		"версия":         badVersion,
		"обрезан CSRC":   truncatedCSRC,
		"обрезано расш.": truncatedExt,
		"padding":        badPadding,
	}

	for name, data := range cases {
		if _, _, err := ParsePacket(data); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

// goertzel возвращает мощность составляющей с частотой freq в отсчётах
func goertzel(samples []int16, freq float64, rate int) float64 {
	k := 2 * math.Cos(2*math.Pi*freq/float64(rate))

	var s1, s2 float64

	for _, x := range samples {
		s0 := float64(x) + k*s1 - s2
		s2, s1 = s1, s0
	}

	return (s1*s1 + s2*s2 - k*s1*s2) / float64(len(samples))
}

func TestWAVWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.wav")

	w, err := createWAV(path, 8000, 1)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.write([]int16{1, -2, 300}); err != nil {
		t.Fatal(err)
	}

	if err := w.close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) != wavHeaderLen+6 || string(data[0:4]) != "RIFF" || string(data[8:16]) != "WAVEfmt " || string(data[36:40]) != "data" {
		t.Fatalf("неверная структура WAV: %d байт, %q", len(data), data[:44])
	}

	if binary.LittleEndian.Uint32(data[4:]) != 36+6 || binary.LittleEndian.Uint32(data[40:]) != 6 {
		t.Error("размеры в заголовке не совпадают с данными")
	}

	if binary.LittleEndian.Uint32(data[24:]) != 8000 || binary.LittleEndian.Uint16(data[22:]) != 1 || binary.LittleEndian.Uint16(data[34:]) != 16 {
		t.Error("формат: ожидалось 8000 Гц, моно, 16 бит")
	}

	if int16(binary.LittleEndian.Uint16(data[46:])) != -2 {
		t.Error("отсчёты записаны неверно")
	}
}

func TestWAVCreateError(t *testing.T) {
	if _, err := createWAV(filepath.Join(t.TempDir(), "нет", "такой", "папки.wav"), 8000, 1); err == nil {
		t.Error("ожидалась ошибка создания файла")
	}
}
