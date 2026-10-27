package rtp

import (
	"strings"
	"testing"

	"tester_mm/internal/sdp"
)

func mustCodec(t *testing.T, spec string) Codec {
	t.Helper()

	c, err := ParseCodec(spec)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestParseCodec(t *testing.T) {
	cases := []struct {
		spec string
		want string
		pt   int
		rate int
		ch   int
	}{
		{"PCMU", "PCMU", 0, 8000, 1},
		{"pcma", "PCMA", 8, 8000, 1},
		{" G722 ", "G722", 9, 8000, 1},
		{"g722/8000", "G722", 9, 8000, 1},
		{"PCMU/8000/1", "PCMU", 0, 8000, 1},
		{"L16", "L16/8000/1", -1, 8000, 1},
		{"L16/16000", "L16/16000/1", -1, 16000, 1},
		{"l16/48000/2", "L16/48000/2", -1, 48000, 2},
		{"L16/44100", "L16/44100/1", 11, 44100, 1},
		{"L16/44100/2", "L16/44100/2", 10, 44100, 2},
		{"telephone-event", "telephone-event", -1, 8000, 1},
	}

	for _, c := range cases {
		got, err := ParseCodec(c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}

		if got.String() != c.want || got.StaticPT != c.pt || got.ClockRate != c.rate || got.Channels != c.ch {
			t.Errorf("%q: %s pt=%d rate=%d ch=%d, ожидалось %s %d %d %d", c.spec, got, got.StaticPT, got.ClockRate, got.Channels, c.want, c.pt, c.rate, c.ch)
		}
	}

	if g := G722; g.ClockRate != 8000 || g.SampleRate != 16000 {
		t.Errorf("G.722: часы %d, звук %d: ожидалось 8000 и 16000 (так записано в SDP)", g.ClockRate, g.SampleRate)
	}
}

func TestParseCodecErrors(t *testing.T) {
	cases := map[string]string{
		"opus":           "не поддерживается",
		"":               "не поддерживается",
		"PCMU/16000":     "частота 8000",
		"PCMA/8000/2":    "один канал",
		"G722/16000":     "частота 8000",
		"L16/12345":      "частота 12345",
		"L16/16000/3":    "каналов",
		"L16/16000/2/9":  "слишком много",
		"L16/abc":        "не число",
		"PCMU/8000/1/5":  "частота 8000",
		"L16/16000/zero": "не число",
	}

	for spec, want := range cases {
		_, err := ParseCodec(spec)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ожидалась ошибка с %q, получили: %v", spec, want, err)
		}
	}
}

func TestMatchSDP(t *testing.T) {
	cases := []struct {
		name  string
		rate  int
		ch    int
		want  string
		match bool
	}{
		{"PCMU", 8000, 0, "PCMU", true},
		{"pcma", 8000, 1, "PCMA", true},
		{"G722", 8000, 1, "G722", true},
		{"L16", 16000, 1, "L16/16000/1", true},
		{"L16", 44100, 2, "L16/44100/2", true},
		{"PCMU", 16000, 1, "", false},
		{"PCMA", 8000, 2, "", false},
		{"G722", 16000, 1, "", false},
		{"L16", 12345, 1, "", false},
		{"opus", 48000, 2, "", false},
	}

	for _, c := range cases {
		got, ok := MatchSDP(c.name, c.rate, c.ch)
		if ok != c.match || (ok && got.String() != c.want) {
			t.Errorf("%s/%d/%d: %s %v, ожидалось %s %v", c.name, c.rate, c.ch, got, ok, c.want, c.match)
		}
	}

	if ev, ok := MatchSDP("Telephone-Event", 8000, 1); !ok || ev.IsAudio() {
		t.Errorf("telephone-event должен находиться и не считаться звуком: %+v %v", ev, ok)
	}
}

func TestPayloadSizes(t *testing.T) {
	cases := []struct {
		spec     string
		ptime    int
		samples  int
		bytes    int
		clock    int
		hasError bool
	}{
		{"PCMU", 20, 160, 160, 160, false},
		{"PCMA", 10, 80, 80, 80, false},
		{"G722", 20, 320, 160, 160, false}, // звук 16 кГц, а часы RTP — 8 кГц
		{"G722", 10, 160, 80, 80, false},
		{"L16/16000", 20, 320, 640, 320, false},
		{"L16/44100/2", 20, 882, 3528, 882, false},
		{"L16/8000", 30, 240, 480, 240, false},
		{"L16/11025", 20, 0, 0, 0, true}, // 220.5 отсчёта
		{"L16/44100", 1, 0, 0, 0, true},  // 44.1 отсчёта
	}

	for _, c := range cases {
		codec := mustCodec(t, c.spec)

		samples, err := codec.FrameSamples(c.ptime)
		if c.hasError {
			if err == nil {
				t.Errorf("%s %d мс: ожидалась ошибка нецелого числа отсчётов", c.spec, c.ptime)
			}

			continue
		}

		bytes, _ := codec.PayloadBytes(c.ptime)

		if err != nil || samples != c.samples || bytes != c.bytes || codec.ClockPerFrame(c.ptime) != c.clock {
			t.Errorf("%s %d мс: отсчётов %d, байт %d, часов %d; ожидалось %d, %d, %d", c.spec, c.ptime, samples, bytes, codec.ClockPerFrame(c.ptime), c.samples, c.bytes, c.clock)
		}
	}
}

func TestL16BigEndianAndRoundTrip(t *testing.T) {
	codec := mustCodec(t, "L16/16000")

	got := codec.NewEncoder().Encode([]int16{1, -2, 0x1234}, nil)
	if string(got) != "\x00\x01\xff\xfe\x12\x34" {
		t.Errorf("L16 должен идти старшим байтом вперёд, получили % x", got)
	}

	back := codec.NewDecoder().Decode(got, nil)
	if len(back) != 3 || back[0] != 1 || back[1] != -2 || back[2] != 0x1234 {
		t.Errorf("обратное преобразование: %v", back)
	}
}

func TestEventCodecHasNoCoder(t *testing.T) {
	if TelephoneEvent.NewEncoder() != nil || TelephoneEvent.NewDecoder() != nil {
		t.Error("у telephone-event нет кодера и декодера")
	}
}

// ===== Цикл тона =====

func TestToneLoopSizes(t *testing.T) {
	cases := []struct {
		spec   string
		ptime  int
		frames int
		size   int
	}{
		{"PCMU", 20, 50, 160},
		{"PCMA", 10, 100, 80},
		{"G722", 20, 50, 160},
		{"L16/16000", 20, 50, 640},
		{"PCMU", 30, 100, 240}, // 30 мс не укладывается в секунду: цикл 3 секунды
		{"L16/44100/2", 20, 50, 3528},
	}

	for _, c := range cases {
		loop, err := toneLoop(mustCodec(t, c.spec), c.ptime, 440)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}

		if len(loop.frames) != c.frames {
			t.Errorf("%s %d мс: кадров %d, ожидалось %d", c.spec, c.ptime, len(loop.frames), c.frames)
		}

		for i, frame := range loop.frames {
			if len(frame) != c.size {
				t.Fatalf("%s: кадр %d имеет %d байт, ожидалось %d", c.spec, i, len(frame), c.size)
			}
		}
	}
}

func decodeLoop(t *testing.T, c Codec, ptime int, loop *frameLoop, repeats int) []int16 {
	t.Helper()

	dec := c.NewDecoder()

	var out []int16

	for r := 0; r < repeats; r++ {
		for _, frame := range loop.frames {
			out = append(out, dec.Decode(frame, nil)...)
		}
	}

	return out
}

// Закодированный цикл даёт чистый тон нужной частоты и после повтора
func TestToneLoopFrequency(t *testing.T) {
	for _, spec := range []string{"PCMU", "PCMA", "G722", "L16/8000", "L16/16000"} {
		codec := mustCodec(t, spec)

		loop, err := toneLoop(codec, 20, 1000)
		if err != nil {
			t.Fatal(err)
		}

		// три повтора: стык циклов не должен давать заметных искажений
		samples := decodeLoop(t, codec, 20, loop, 3)
		samples = samples[len(samples)/3:]

		own := goertzel(samples, 1000, codec.SampleRate)
		other := goertzel(samples, 1300, codec.SampleRate)

		if own < other*100 {
			t.Errorf("%s: 1000 Гц %v против 1300 Гц %v", spec, own, other)
		}
	}
}

// Стереозвук: обе дорожки одинаковы
func TestToneLoopStereo(t *testing.T) {
	codec := mustCodec(t, "L16/16000/2")

	loop, err := toneLoop(codec, 20, 500)
	if err != nil {
		t.Fatal(err)
	}

	samples := decodeLoop(t, codec, 20, loop, 1)
	for i := 0; i+1 < len(samples); i += 2 {
		if samples[i] != samples[i+1] {
			t.Fatalf("отсчёт %d: каналы различаются %d и %d", i/2, samples[i], samples[i+1])
		}
	}
}

func TestToneLoopSilenceAndCache(t *testing.T) {
	loop, err := toneLoop(PCMU, 20, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, frame := range loop.frames {
		for _, b := range frame {
			if b != 0xFF {
				t.Fatalf("тишина PCMU — байты 0xFF, получили 0x%02X", b)
			}
		}
	}

	again, _ := toneLoop(PCMU, 20, 0)
	if again != loop {
		t.Error("повторный запрос должен возвращать тот же цикл из кэша")
	}

	other, _ := toneLoop(PCMU, 20, 440)
	if other == loop {
		t.Error("другая частота — другой цикл")
	}

	if _, err := toneLoop(PCMU, 20, -5); err == nil {
		t.Error("отрицательная частота должна давать ошибку")
	}

	if _, err := toneLoop(mustCodec(t, "L16/11025"), 20, 440); err == nil {
		t.Error("ptime, не дающий целого числа отсчётов, должен давать ошибку")
	}
}

// ===== Назначение payload type и SDP =====

func TestAssignPayloadTypes(t *testing.T) {
	codecs := []Codec{PCMA, G722, mustCodec(t, "L16/16000"), TelephoneEvent, mustCodec(t, "L16/48000/2")}

	got, err := assignPayloadTypes(codecs, 96)
	if err != nil {
		t.Fatal(err)
	}

	want := []int{8, 9, 97, 96, 98} // 96 занят telephone-event, динамические идут с 97
	for i, w := range want {
		if got[i].PayloadType != w {
			t.Errorf("%s: payload type %d, ожидалось %d", codecs[i], got[i].PayloadType, w)
		}
	}

	if got[3].Fmtp != "0-15" || got[0].Fmtp != "" {
		t.Errorf("fmtp: у telephone-event %q, у PCMA %q", got[3].Fmtp, got[0].Fmtp)
	}

	// telephone-event по умолчанию получает 101
	def, _ := assignPayloadTypes([]Codec{PCMU, TelephoneEvent}, 0)
	if def[1].PayloadType != DefaultDTMFPT {
		t.Errorf("telephone-event по умолчанию: %d", def[1].PayloadType)
	}
}

func TestAssignPayloadTypesErrors(t *testing.T) {
	cases := []struct {
		name   string
		codecs []Codec
		dtmf   int
		want   string
	}{
		{"дубликат", []Codec{PCMU, PCMU}, 0, "дважды"},
		{"telephone-event на PCMA", []Codec{PCMA, TelephoneEvent}, 8, "нужен и для"},
		{"вне диапазона", []Codec{PCMU, TelephoneEvent}, 200, "вне диапазона"},
		{"отрицательный", []Codec{TelephoneEvent}, -3, "вне диапазона"},
	}

	for _, c := range cases {
		_, err := assignPayloadTypes(c.codecs, c.dtmf)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

// SDP, который собирает тестер, разбирается обратно в те же кодеки и payload type
func TestOfferRoundTripThroughSDP(t *testing.T) {
	f := newTestFactory(t)

	s := openSession(t, f, OpenOptions{
		Codecs: []Codec{G722, mustCodec(t, "L16/16000/2"), PCMA, TelephoneEvent},
		DTMFPT: 96,
		PTime:  20,
	})

	for _, want := range []string{
		"m=audio ",
		" RTP/AVP 9 97 8 96\r\n",
		"a=rtpmap:9 G722/8000\r\n",
		"a=rtpmap:97 L16/16000/2\r\n",
		"a=rtpmap:8 PCMA/8000\r\n",
		"a=rtpmap:96 telephone-event/8000\r\n",
		"a=fmtp:96 0-15\r\n",
	} {
		if !strings.Contains(s.Offer(), want) {
			t.Errorf("в SDP нет %q:\n%s", want, s.Offer())
		}
	}

	desc, err := sdp.Parse(s.Offer())
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := desc.Audio()
	codec, pt, err := chooseCodec(audio)

	if err != nil || codec.Name != "G722" || pt != 9 {
		t.Errorf("первым подходящим должен быть G722/9: %s %d %v", codec, pt, err)
	}

	if got := chooseEventPT(audio, codec); got != 96 {
		t.Errorf("telephone-event: %d, ожидалось 96", got)
	}
}

func TestChooseCodecFromAnswer(t *testing.T) {
	parse := func(text string) *sdp.Media {
		desc, err := sdp.Parse(text)
		if err != nil {
			t.Fatal(err)
		}

		audio, _ := desc.Audio()

		return audio
	}

	// opus не умеем, дальше идёт L16/16000 с динамическим номером
	audio := parse(answerSDP(4000, "111 96 0", "a=rtpmap:111 opus/48000/2", "a=rtpmap:96 L16/16000/1"))

	codec, pt, err := chooseCodec(audio)
	if err != nil || codec.String() != "L16/16000/1" || pt != 96 {
		t.Errorf("ожидался L16/16000/1 с типом 96: %s %d %v", codec, pt, err)
	}

	// telephone-event сам по себе звуковым кодеком не считается
	_, _, err = chooseCodec(parse(answerSDP(4000, "101", "a=rtpmap:101 telephone-event/8000")))
	if err == nil || !strings.Contains(err.Error(), "нет поддерживаемого кодека") {
		t.Errorf("только telephone-event: %v", err)
	}

	// несколько telephone-event: берётся с частотой звука
	two := parse(answerSDP(4000, "9 97 98", "a=rtpmap:97 telephone-event/16000", "a=rtpmap:98 telephone-event/8000"))
	if got := chooseEventPT(two, G722); got != 98 {
		t.Errorf("для G722 (часы 8000) ожидался 98, получили %d", got)
	}

	if got := chooseEventPT(parse(answerSDP(4000, "0")), PCMU); got != -1 {
		t.Errorf("без telephone-event ожидалось -1, получили %d", got)
	}
}

// Второе плечо звонка: MM прислал offer со своим набором кодеков, а мы ответили только G722.
// Звук должен идти на G722 (payload type 9), а не на первом кодеке из SDP MM (PCMA).
func TestChooseCodecForOfferFromMM(t *testing.T) {
	parse := func(text string) *sdp.Media {
		desc, err := sdp.Parse(text)
		if err != nil {
			t.Fatal(err)
		}

		audio, _ := desc.Audio()

		return audio
	}

	mmOffer := parse(answerSDP(4000, "8 0 9 104 120",
		"a=rtpmap:104 AMR/8000/1", "a=rtpmap:120 telephone-event/8000"))

	codec, pt, err := chooseCodecFor(mmOffer, []Codec{G722, TelephoneEvent})
	if err != nil || codec.Name != "G722" || pt != 9 {
		t.Errorf("offer MM, ответили G722: %s %d %v; ждали G722/9", codec, pt, err)
	}

	// порядок нашего списка главнее порядка MM
	codec, pt, err = chooseCodecFor(mmOffer, []Codec{PCMU, G722})
	if err != nil || codec.Name != "PCMU" || pt != 0 {
		t.Errorf("наш порядок PCMU,G722: %s %d %v; ждали PCMU/0", codec, pt, err)
	}

	// ни одного общего кодека: как раньше, берётся первый кодек MM (несовпадение видно по счётчикам payload type)
	codec, pt, err = chooseCodecFor(parse(answerSDP(4000, "8 0")), []Codec{G722})
	if err != nil || codec.Name != "PCMA" || pt != 8 {
		t.Errorf("нет общих кодеков: %s %d %v; ждали PCMA/8", codec, pt, err)
	}

	// ответ MM на наш offer: берётся первый кодек MM, как раньше
	answer := parse(answerSDP(4000, "0 8"))

	codec, pt, err = chooseCodecFor(answer, []Codec{PCMA, PCMU})
	if err != nil || codec.Name != "PCMU" || pt != 0 {
		t.Errorf("ответ MM «0 8»: %s %d %v; ждали PCMU/0 (порядок MM)", codec, pt, err)
	}

	// SDP задан готовым (кодеки неизвестны): поведение прежнее
	codec, pt, err = chooseCodecFor(mmOffer, nil)
	if err != nil || codec.Name != "PCMA" || pt != 8 {
		t.Errorf("без списка предложенных: %s %d %v; ждали PCMA/8", codec, pt, err)
	}
}
