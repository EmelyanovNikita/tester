package rtp

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

type codecKind int

const (
	kindULaw codecKind = iota
	kindALaw
	kindG722
	kindL16
	kindEvent // telephone-event (DTMF): не звук
)

// Codec — формат полезной нагрузки RTP, который умеет тестер
type Codec struct {
	Name       string // как в SDP: PCMU, PCMA, G722, L16, telephone-event
	kind       codecKind
	StaticPT   int // статический payload type; -1, если назначается динамически
	ClockRate  int // частота RTP-часов (как в a=rtpmap)
	SampleRate int // частота звука; у G.722 это 16000 при ClockRate 8000
	Channels   int
}

var (
	// PCMU — G.711 μ-law
	PCMU = Codec{Name: "PCMU", kind: kindULaw, StaticPT: 0, ClockRate: 8000, SampleRate: 8000, Channels: 1}
	// PCMA — G.711 A-law
	PCMA = Codec{Name: "PCMA", kind: kindALaw, StaticPT: 8, ClockRate: 8000, SampleRate: 8000, Channels: 1}
	// G722 — G.722, 64 кбит/с
	G722 = Codec{Name: "G722", kind: kindG722, StaticPT: 9, ClockRate: 8000, SampleRate: 16000, Channels: 1}
	// TelephoneEvent — DTMF по RFC 4733
	TelephoneEvent = Codec{Name: "telephone-event", kind: kindEvent, StaticPT: -1, ClockRate: 8000, SampleRate: 8000, Channels: 1}
)

// DefaultOffer — кодеки в SDP, если они не заданы
var DefaultOffer = []Codec{PCMU, PCMA}

var l16Rates = []int{8000, 11025, 16000, 22050, 24000, 32000, 44100, 48000}

// L16 — несжатый 16-битный PCM (RFC 3551) с заданной частотой и числом каналов (1 или 2)
func L16(rate, channels int) (Codec, error) {
	if !containsInt(l16Rates, rate) {
		return Codec{}, fmt.Errorf("L16: частота %d не поддерживается (доступны %v)", rate, l16Rates)
	}

	if channels != 1 && channels != 2 {
		return Codec{}, fmt.Errorf("L16: число каналов должно быть 1 или 2, получено %d", channels)
	}

	pt := -1

	if rate == 44100 {
		pt = 11
		if channels == 2 {
			pt = 10
		}
	}

	return Codec{Name: "L16", kind: kindL16, StaticPT: pt, ClockRate: rate, SampleRate: rate, Channels: channels}, nil
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}

	return false
}

// IsAudio сообщает, что кодек несёт звук (telephone-event — нет)
func (c Codec) IsAudio() bool { return c.kind != kindEvent }

// String даёт имя вместе с параметрами там, где они важны: L16/16000/2
func (c Codec) String() string {
	if c.kind == kindL16 {
		return fmt.Sprintf("L16/%d/%d", c.SampleRate, c.Channels)
	}

	return c.Name
}

// SupportedList описывает допустимые значения для сообщений об ошибках
const SupportedList = "PCMU, PCMA, G722, L16[/частота[/каналы]], telephone-event"

// ParseCodec разбирает запись вида "PCMA", "g722", "L16/16000", "L16/44100/2", "telephone-event"
func ParseCodec(spec string) (Codec, error) {
	parts := strings.Split(strings.TrimSpace(spec), "/")

	var numbers []int

	for _, part := range parts[1:] {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return Codec{}, fmt.Errorf("кодек %q: %q не число", spec, part)
		}

		numbers = append(numbers, n)
	}

	name := strings.ToLower(strings.TrimSpace(parts[0]))

	switch name {
	case "l16":
		rate, channels := 8000, 1
		if len(numbers) > 0 {
			rate = numbers[0]
		}

		if len(numbers) > 1 {
			channels = numbers[1]
		}

		if len(numbers) > 2 {
			return Codec{}, fmt.Errorf("кодек %q: слишком много параметров", spec)
		}

		return L16(rate, channels)
	case "pcmu", "pcma", "g722", "telephone-event":
		fixed := map[string]Codec{"pcmu": PCMU, "pcma": PCMA, "g722": G722, "telephone-event": TelephoneEvent}[name]

		if len(numbers) > 2 || (len(numbers) > 0 && numbers[0] != fixed.ClockRate) || (len(numbers) > 1 && numbers[1] != 1) {
			return Codec{}, fmt.Errorf("кодек %q: у %s частота %d и один канал", spec, fixed.Name, fixed.ClockRate)
		}

		return fixed, nil
	}

	return Codec{}, fmt.Errorf("кодек %q не поддерживается (поддерживаются: %s)", spec, SupportedList)
}

// MatchSDP подбирает кодек тестера по записи a=rtpmap из SDP: имя, частота, число каналов (0 — не указано)
func MatchSDP(name string, clockRate, channels int) (Codec, bool) {
	if channels == 0 {
		channels = 1
	}

	switch strings.ToLower(name) {
	case "pcmu":
		return PCMU, clockRate == PCMU.ClockRate && channels == 1
	case "pcma":
		return PCMA, clockRate == PCMA.ClockRate && channels == 1
	case "g722":
		return G722, clockRate == G722.ClockRate && channels == 1
	case "l16":
		c, err := L16(clockRate, channels)
		return c, err == nil
	case "telephone-event":
		c := TelephoneEvent
		c.ClockRate = clockRate
		c.SampleRate = clockRate

		return c, true
	}

	return Codec{}, false
}

// FrameSamples возвращает число отсчётов на канал в пакете длиной ptime мс
func (c Codec) FrameSamples(ptimeMs int) (int, error) {
	if c.SampleRate*ptimeMs%1000 != 0 || c.ClockRate*ptimeMs%1000 != 0 {
		return 0, fmt.Errorf("ptime %d мс не даёт целого числа отсчётов для %s", ptimeMs, c)
	}

	return c.SampleRate * ptimeMs / 1000, nil
}

// ClockPerFrame — на сколько растёт RTP timestamp за один пакет длиной ptime мс
func (c Codec) ClockPerFrame(ptimeMs int) int {
	return c.ClockRate * ptimeMs / 1000
}

// PayloadBytes — размер нагрузки пакета в байтах
func (c Codec) PayloadBytes(ptimeMs int) (int, error) {
	samples, err := c.FrameSamples(ptimeMs)
	if err != nil {
		return 0, err
	}

	switch c.kind {
	case kindG722:
		return samples / 2, nil
	case kindL16:
		return samples * c.Channels * 2, nil
	case kindEvent:
		return 4, nil
	}

	return samples, nil
}

// Encoder кодирует кадр отсчётов (при двух каналах — с чередованием) в нагрузку RTP; хранит состояние между кадрами
type Encoder interface {
	Encode(pcm []int16, dst []byte) []byte
}

// Decoder превращает нагрузку RTP в отсчёты; хранит состояние между пакетами
type Decoder interface {
	Decode(payload []byte, dst []int16) []int16
}

// NewEncoder создаёт кодер; для telephone-event его нет
func (c Codec) NewEncoder() Encoder {
	switch c.kind {
	case kindULaw:
		return law{encode: encodeULaw, decode: decodeULaw}
	case kindALaw:
		return law{encode: encodeALaw, decode: decodeALaw}
	case kindG722:
		return newG722Encoder()
	case kindL16:
		return linear16{}
	}

	return nil
}

// NewDecoder создаёт декодер; для telephone-event его нет
func (c Codec) NewDecoder() Decoder {
	switch c.kind {
	case kindULaw:
		return law{encode: encodeULaw, decode: decodeULaw}
	case kindALaw:
		return law{encode: encodeALaw, decode: decodeALaw}
	case kindG722:
		return newG722Decoder()
	case kindL16:
		return linear16{}
	}

	return nil
}

// law — G.711: один байт на отсчёт, состояния нет
type law struct {
	encode func(int16) byte
	decode func(byte) int16
}

func (l law) Encode(pcm []int16, dst []byte) []byte {
	dst = dst[:0]
	for _, s := range pcm {
		dst = append(dst, l.encode(s))
	}

	return dst
}

func (l law) Decode(payload []byte, dst []int16) []int16 {
	dst = dst[:0]
	for _, b := range payload {
		dst = append(dst, l.decode(b))
	}

	return dst
}

// linear16 — L16: 16 бит со знаком, старший байт первым (сетевой порядок)
type linear16 struct{}

func (linear16) Encode(pcm []int16, dst []byte) []byte {
	dst = dst[:0]
	for _, s := range pcm {
		dst = binary.BigEndian.AppendUint16(dst, uint16(s))
	}

	return dst
}

func (linear16) Decode(payload []byte, dst []int16) []int16 {
	dst = dst[:0]
	for i := 0; i+1 < len(payload); i += 2 {
		dst = append(dst, int16(binary.BigEndian.Uint16(payload[i:])))
	}

	return dst
}
