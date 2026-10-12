package rtp

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturePackets собирает датаграммы с сокета «удалённой стороны» в течение d
func capturePackets(conn *net.UDPConn, d time.Duration) [][]byte {
	deadline := time.Now().Add(d)
	buf := make([]byte, 4096)

	var packets [][]byte

	for {
		conn.SetReadDeadline(deadline)

		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return packets
		}

		packets = append(packets, append([]byte(nil), buf[:n]...))
	}
}

type wavInfo struct {
	rate, channels int
	samples        []int16
}

func readWAV(t *testing.T, path string) wavInfo {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) < wavHeaderLen {
		t.Fatalf("%s: короче заголовка WAV", path)
	}

	info := wavInfo{
		channels: int(binary.LittleEndian.Uint16(data[22:])),
		rate:     int(binary.LittleEndian.Uint32(data[24:])),
	}

	for i := wavHeaderLen; i+1 < len(data); i += 2 {
		info.samples = append(info.samples, int16(binary.LittleEndian.Uint16(data[i:])))
	}

	return info
}

// channel0 выбирает отсчёты первого канала
func (w wavInfo) channel0() []int16 {
	var out []int16
	for i := 0; i < len(w.samples); i += w.channels {
		out = append(out, w.samples[i])
	}

	return out
}

// ===== Формат пакетов на проводе =====

func TestSendFormatPerCodec(t *testing.T) {
	cases := []struct {
		spec    string
		rtpmap  string
		pt      int
		payload int
		step    uint32
	}{
		{"PCMA", "a=rtpmap:8 PCMA/8000", 8, 160, 160},
		{"G722", "a=rtpmap:9 G722/8000", 9, 160, 160}, // 320 отсчётов 16 кГц = 160 байт, часы RTP идут по 8 кГц
		{"L16/16000", "a=rtpmap:97 L16/16000/1", 97, 640, 320},
		{"L16/44100/2", "a=rtpmap:10 L16/44100/2", 10, 3528, 882},
	}

	f := newTestFactory(t)

	for _, c := range cases {
		remote := listener(t)
		s := openSession(t, f, OpenOptions{})

		sdpText := answerSDP(portOf(remote), fmt.Sprint(c.pt), c.rtpmap, "a=ptime:20")
		if err := s.Start(StartOptions{RemoteSDP: sdpText}); err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}

		packets := capturePackets(remote, 200*time.Millisecond)
		s.Stop()

		if len(packets) < 5 {
			t.Fatalf("%s: пришло %d пакетов", c.spec, len(packets))
		}

		var prev Header

		for i, raw := range packets {
			h, payload, err := ParsePacket(raw)
			if err != nil {
				t.Fatal(err)
			}

			if int(h.PayloadType) != c.pt || len(payload) != c.payload {
				t.Fatalf("%s пакет %d: pt=%d, нагрузка %d байт; ожидалось %d и %d", c.spec, i, h.PayloadType, len(payload), c.pt, c.payload)
			}

			if h.Marker != (i == 0) {
				t.Errorf("%s пакет %d: marker=%v", c.spec, i, h.Marker)
			}

			if i > 0 && (h.Seq != prev.Seq+1 || h.Timestamp != prev.Timestamp+c.step || h.SSRC != prev.SSRC) {
				t.Errorf("%s пакет %d: seq %d→%d, ts %d→%d (шаг должен быть %d)", c.spec, i, prev.Seq, h.Seq, prev.Timestamp, h.Timestamp, c.step)
			}

			prev = h
		}
	}
}

// ===== Два сеанса друг с другом на разных кодеках =====

func TestLoopbackPerCodec(t *testing.T) {
	cases := []struct {
		spec string
		rate int
		ch   int
	}{
		{"G722", 16000, 1},
		{"L16/16000", 16000, 1},
		{"L16/8000/2", 8000, 2},
		{"PCMU", 8000, 1},
	}

	f := newTestFactory(t)

	for _, c := range cases {
		codec := mustCodec(t, c.spec)
		dir := t.TempDir()

		a := openSession(t, f, OpenOptions{Codecs: []Codec{codec}})
		b := openSession(t, f, OpenOptions{Codecs: []Codec{codec}})

		recA, recB := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")

		if err := a.Start(StartOptions{RemoteSDP: b.Offer(), ToneHz: 440, Record: recA}); err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}

		if err := b.Start(StartOptions{RemoteSDP: a.Offer(), ToneHz: 1000, Record: recB}); err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}

		time.Sleep(600 * time.Millisecond)

		statsA, statsB := a.Stop(), b.Stop()

		for name, st := range map[string]Stats{"A": statsA, "B": statsB} {
			if st.ReceivedPackets < 15 || st.LostPackets > 1 || st.Codec != codec.String() {
				t.Errorf("%s %s: принято %d, потеряно %d, кодек %s", c.spec, name, st.ReceivedPackets, st.LostPackets, st.Codec)
			}
		}

		wavA, wavB := readWAV(t, recA), readWAV(t, recB)

		if wavA.rate != c.rate || wavA.channels != c.ch || wavB.rate != c.rate || wavB.channels != c.ch {
			t.Errorf("%s: WAV %d Гц %d кан. и %d Гц %d кан., ожидалось %d Гц %d кан.", c.spec, wavA.rate, wavA.channels, wavB.rate, wavB.channels, c.rate, c.ch)
		}

		heardByA, heardByB := wavA.channel0(), wavB.channel0()
		if len(heardByA) < c.rate/4 || len(heardByB) < c.rate/4 {
			t.Fatalf("%s: записи слишком короткие: %d и %d отсчётов", c.spec, len(heardByA), len(heardByB))
		}

		if goertzel(heardByA, 1000, c.rate) < goertzel(heardByA, 440, c.rate)*50 {
			t.Errorf("%s: в записи A должен слышаться тон 1000 Гц", c.spec)
		}

		if goertzel(heardByB, 440, c.rate) < goertzel(heardByB, 1000, c.rate)*50 {
			t.Errorf("%s: в записи B должен слышаться тон 440 Гц", c.spec)
		}
	}
}

func TestToneMustBeWholeHertz(t *testing.T) {
	s := openSession(t, newTestFactory(t), OpenOptions{})

	err := s.Start(StartOptions{RemoteSDP: answerSDP(4000, "0"), ToneHz: 440.5})
	if err == nil || !strings.Contains(err.Error(), "целым числом") {
		t.Fatalf("ожидалась ошибка про целое число Гц, получили: %v", err)
	}
}

// ===== DTMF =====

const eventAnswerExtra = "a=rtpmap:101 telephone-event/8000"

func eventAnswer(port int) string {
	return answerSDP(port, "0 101", eventAnswerExtra, "a=fmtp:101 0-15", "a=ptime:20")
}

// События на проводе устроены по RFC 4733: общий timestamp, растущая длительность, три пакета окончания,
// нумерация общая со звуком, а звук после события продолжается с правильным timestamp
func TestDTMFOnTheWire(t *testing.T) {
	remote := listener(t)
	s := openSession(t, newTestFactory(t), OpenOptions{})

	if err := s.Start(StartOptions{RemoteSDP: eventAnswer(portOf(remote))}); err != nil {
		t.Fatal(err)
	}

	var (
		packets [][]byte
		wg      sync.WaitGroup
	)

	wg.Add(1)

	go func() {
		defer wg.Done()
		packets = capturePackets(remote, 900*time.Millisecond)
	}()

	time.Sleep(60 * time.Millisecond) // сначала немного звука

	if err := s.SendDTMF("5#", 100, 40, 12); err != nil {
		t.Fatal(err)
	}

	wg.Wait()
	s.Stop()

	type packet struct {
		h       Header
		payload []byte
	}

	var all []packet

	for _, raw := range packets {
		h, payload, err := ParsePacket(raw)
		if err != nil {
			t.Fatal(err)
		}

		all = append(all, packet{h, payload})
	}

	// нумерация общая и без пропусков: и у звука, и у событий
	for i := 1; i < len(all); i++ {
		if all[i].h.Seq != all[i-1].h.Seq+1 {
			t.Fatalf("пакет %d: seq %d после %d", i, all[i].h.Seq, all[i-1].h.Seq)
		}
	}

	var events []packet

	for _, p := range all {
		if p.h.PayloadType == 101 {
			events = append(events, p)
		}
	}

	// ptime 20, длительность 100 → 5 пакетов, из них последний — окончание, и ещё два повтора окончания
	if len(events) != 2*(5+dtmfEndRepeats-1) {
		t.Fatalf("пакетов событий %d, ожидалось %d", len(events), 2*(5+dtmfEndRepeats-1))
	}

	for digit, want := range []byte{5, 11} {
		group := events[digit*7 : digit*7+7]

		for i, p := range group {
			if len(p.payload) != 4 {
				t.Fatalf("нагрузка события %d байт", len(p.payload))
			}

			if p.h.Timestamp != group[0].h.Timestamp {
				t.Errorf("цифра %d, пакет %d: timestamp %d отличается от первого %d", digit, i, p.h.Timestamp, group[0].h.Timestamp)
			}

			if p.h.Marker != (i == 0) {
				t.Errorf("цифра %d, пакет %d: marker=%v", digit, i, p.h.Marker)
			}

			event, end, duration, _ := parseEventPayload(p.payload)
			if event != want || p.payload[1]&0x3F != 12 {
				t.Errorf("цифра %d, пакет %d: событие %d, громкость %d", digit, i, event, p.payload[1]&0x3F)
			}

			wantEnd := i >= 4
			wantDuration := uint16(160 * min(i+1, 5))

			if end != wantEnd || duration != wantDuration {
				t.Errorf("цифра %d, пакет %d: E=%v длительность %d; ожидалось %v и %d", digit, i, end, duration, wantEnd, wantDuration)
			}
		}
	}

	// звук идёт до события, между цифрами и после; его timestamp продолжается по тикам, а на события тики тоже тратятся
	var audio []packet

	for _, p := range all {
		if p.h.PayloadType == 0 {
			audio = append(audio, p)
		}
	}

	if len(audio) < 5 || audio[0].h.Marker != true {
		t.Fatalf("звуковых пакетов %d", len(audio))
	}

	for i := 1; i < len(audio); i++ {
		if audio[i].h.Marker {
			t.Errorf("звуковой пакет %d: marker не должен ставиться после начала потока", i)
		}
	}

	// каждый тик занимает 160 единиц часов, независимо от того, звук в нём или событие
	for i := 1; i < len(all); i++ {
		if all[i].h.PayloadType != 0 {
			continue
		}

		// ищем предыдущий звуковой пакет и считаем, сколько тиков между ними
		j := i - 1
		for all[j].h.PayloadType != 0 {
			j--
		}

		ticks := uint32(i - j)
		if got := all[i].h.Timestamp - all[j].h.Timestamp; got != 160*ticks {
			t.Errorf("звук после события: timestamp вырос на %d за %d тиков, ожидалось %d", got, ticks, 160*ticks)
		}
	}

	// timestamp события равен timestamp звукового пакета, который стоял бы на этом месте
	first := events[0]

	for i := range all {
		if all[i].h.Seq == first.h.Seq && i > 0 && all[i-1].h.PayloadType == 0 {
			if first.h.Timestamp != all[i-1].h.Timestamp+160 {
				t.Errorf("timestamp события %d, ожидалось %d", first.h.Timestamp, all[i-1].h.Timestamp+160)
			}
		}
	}
}

func TestDTMFLoopback(t *testing.T) {
	f := newTestFactory(t)

	a := openSession(t, f, OpenOptions{Codecs: []Codec{PCMU, TelephoneEvent}})
	b := openSession(t, f, OpenOptions{Codecs: []Codec{PCMU, TelephoneEvent}})

	if err := a.Start(StartOptions{RemoteSDP: b.Offer()}); err != nil {
		t.Fatal(err)
	}

	if err := b.Start(StartOptions{RemoteSDP: a.Offer()}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(80 * time.Millisecond)

	// одинаковые цифры подряд и в разных событиях считаются отдельно
	if err := a.SendDTMF("5505*#d", 60, 20, 10); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	sent, got := a.Stop(), b.Stop()

	if sent.DTMFSent != "5505*#D" {
		t.Errorf("отправлено %q", sent.DTMFSent)
	}

	if got.DTMFReceived != "5505*#D" {
		t.Errorf("принято %q, ожидалось 5505*#D", got.DTMFReceived)
	}

	// 7 цифр по (3 пакета + 2 повтора окончания)
	if got.DTMFPackets != 7*(3+dtmfEndRepeats-1) {
		t.Errorf("пакетов событий %d, ожидалось %d", got.DTMFPackets, 7*(3+dtmfEndRepeats-1))
	}

	if got.LostPackets > 1 || got.ReceivedPackets < 5 {
		t.Errorf("звук рядом с событиями: принято %d, потеряно %d", got.ReceivedPackets, got.LostPackets)
	}

	if got.OtherPayloadPackets != 0 {
		t.Errorf("события не должны попадать в «другие»: %d", got.OtherPayloadPackets)
	}
}

func TestSendDTMFErrors(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	started := func(sdpText string, opts StartOptions) *Session {
		s := openSession(t, f, OpenOptions{})
		opts.RemoteSDP = sdpText

		if err := s.Start(opts); err != nil {
			t.Fatal(err)
		}

		return s
	}

	notStarted := openSession(t, f, OpenOptions{})
	noEvent := started(answerSDP(portOf(remote), "0"), StartOptions{})
	receiveOnly := started(eventAnswer(portOf(remote)), StartOptions{NoSend: true})
	sendOnly := started(eventAnswer(portOf(remote)), StartOptions{})

	stopped := started(eventAnswer(portOf(remote)), StartOptions{})
	stopped.Stop()

	cases := []struct {
		name     string
		s        *Session
		digits   string
		duration int
		gap      int
		volume   int
		want     string
	}{
		{"не запущен", notStarted, "1", 100, 0, 10, "ещё не запущен"},
		{"нет telephone-event в ответе", noEvent, "1", 100, 0, 10, "нет telephone-event"},
		{"только приём", receiveOnly, "1", 100, 0, 10, "не отправляет"},
		{"остановлен", stopped, "1", 100, 0, 10, "остановлен"},
		{"неверный символ", sendOnly, "12x", 100, 0, 10, "недопустимый символ"},
		{"пустая строка", sendOnly, "", 100, 0, 10, "нет цифр"},
		{"длительность 0", sendOnly, "1", 0, 0, 10, "длительность"},
		{"слишком долго", sendOnly, "1", 20000, 0, 10, "длительность"},
		{"пауза", sendOnly, "1", 100, -1, 10, "пауза"},
		{"громкость", sendOnly, "1", 100, 0, 64, "громкость"},
	}

	for _, c := range cases {
		err := c.s.SendDTMF(c.digits, c.duration, c.gap, c.volume)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

func TestSendDTMFInterruptedByStop(t *testing.T) {
	remote := listener(t)
	s := openSession(t, newTestFactory(t), OpenOptions{})

	if err := s.Start(StartOptions{RemoteSDP: eventAnswer(portOf(remote))}); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)

	go func() { result <- s.SendDTMF("1234567890", 2000, 0, 10) }()

	time.Sleep(150 * time.Millisecond)
	s.Stop()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "остановлен") {
			t.Errorf("ожидалась ошибка остановки, получили: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendDTMF не вернулся после Stop")
	}
}

// ===== Приём событий и учёт потерь =====

// feedWithEvents — сеанс без отправки, которому пакеты подаются напрямую; в ответе есть telephone-event
func feedWithEvents(t *testing.T, record string) *Session {
	t.Helper()

	s := openSession(t, newTestFactory(t), OpenOptions{})

	err := s.Start(StartOptions{RemoteSDP: eventAnswer(4000), NoSend: true, Record: record})
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func eventPacket(seq uint16, ts uint32, event byte, end bool, duration uint16) []byte {
	payload := make([]byte, 4)
	payload[0] = event
	payload[1] = 10

	if end {
		payload[1] |= 0x80
	}

	binary.BigEndian.PutUint16(payload[2:], duration)

	return packetAt(1, seq, ts, 101, payload)
}

func TestReceiveEventsKeepSequenceAndCountDigitsOnce(t *testing.T) {
	s := feedWithEvents(t, "")
	now := time.Now()

	s.handlePacket(packetAt(1, 1, 0, 0, pcmuSilence), now)
	s.handlePacket(packetAt(1, 2, 160, 0, pcmuSilence), now)

	// цифра 7: два пакета, затем окончание с двумя повторами; номера продолжают нумерацию звука
	s.handlePacket(eventPacket(3, 320, 7, false, 160), now)
	s.handlePacket(eventPacket(4, 320, 7, false, 320), now)
	s.handlePacket(eventPacket(5, 320, 7, true, 480), now)
	s.handlePacket(eventPacket(6, 320, 7, true, 480), now)
	s.handlePacket(eventPacket(7, 320, 7, true, 480), now)

	// следующая цифра 7 с другим timestamp — это другое событие
	s.handlePacket(eventPacket(8, 960, 7, true, 160), now)

	// событие с неизвестным номером и обрезанная нагрузка
	s.handlePacket(eventPacket(9, 1280, 99, true, 160), now)
	s.handlePacket(packetAt(1, 10, 1600, 101, []byte{1, 2}), now)

	s.handlePacket(packetAt(1, 11, 1760, 0, pcmuSilence), now)

	st := s.Stats()

	if st.DTMFReceived != "77" {
		t.Errorf("принято %q, ожидалось \"77\"", st.DTMFReceived)
	}

	if st.DTMFPackets != 8 || st.ReceivedPackets != 3 || st.BadPackets != 1 {
		t.Errorf("пакетов событий %d, звука %d, плохих %d; ожидалось 8, 3, 1", st.DTMFPackets, st.ReceivedPackets, st.BadPackets)
	}

	if st.LostPackets != 0 || st.LossPercent != 0 {
		t.Errorf("события делят нумерацию со звуком, потерь быть не должно: %d (%v%%)", st.LostPackets, st.LossPercent)
	}
}

// Потерянный пакет события виден как потеря, а цифра всё равно засчитывается по другим пакетам события
func TestReceiveLostEventPacket(t *testing.T) {
	s := feedWithEvents(t, "")
	now := time.Now()

	s.handlePacket(eventPacket(1, 0, 3, false, 160), now)
	s.handlePacket(eventPacket(3, 0, 3, true, 480), now) // пакет 2 потерян

	st := s.Stats()
	if st.DTMFReceived != "3" || st.LostPackets != 1 {
		t.Errorf("принято %q, потеряно %d; ожидалось \"3\" и 1", st.DTMFReceived, st.LostPackets)
	}
}

// В записи звука место события заполняется тишиной по timestamp, чтобы время не «схлопывалось»
func TestRecordFillsEventGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.wav")
	s := feedWithEvents(t, path)
	now := time.Now()

	loud := make([]byte, 160)
	for i := range loud {
		loud[i] = encodeULaw(6000)
	}

	s.handlePacket(packetAt(1, 1, 0, 0, loud), now)
	s.handlePacket(eventPacket(2, 160, 5, true, 160), now)
	s.handlePacket(eventPacket(3, 160, 5, true, 160), now)
	s.handlePacket(packetAt(1, 4, 480, 0, loud), now) // два тика (320 единиц) заняты событием

	s.Stop()

	wav := readWAV(t, path)
	if len(wav.samples) != 640 {
		t.Fatalf("в записи %d отсчётов, ожидалось 640 (звук, 320 отсчётов тишины, звук)", len(wav.samples))
	}

	if wav.samples[100] < 4000 || wav.samples[200] != 0 || wav.samples[400] != 0 || wav.samples[500] < 4000 {
		t.Errorf("запись: %d %d %d %d", wav.samples[100], wav.samples[200], wav.samples[400], wav.samples[500])
	}
}

// ===== Округление =====

func TestStatsRounding(t *testing.T) {
	s := feedSession(t, StartOptions{})
	now := time.Now()

	// получено 2 из 3 номеров: потеряна треть
	s.handlePacket(packetAt(1, 0, 0, 0, pcmuSilence), now)
	s.handlePacket(packetAt(1, 2, 320, 0, pcmuSilence), now.Add(40*time.Millisecond+1234*time.Nanosecond))

	st := s.Stats()

	if st.LossPercent != 33.333 {
		t.Errorf("lossPercent = %v, ожидалось 33.333", st.LossPercent)
	}

	if math.Abs(st.JitterMs*1000-math.Round(st.JitterMs*1000)) > 1e-9 {
		t.Errorf("jitterMs = %v: ожидалось не больше трёх знаков", st.JitterMs)
	}
}

// Диагностика: несколько SSRC на одном порту и чужие payload type видны в счётчиках
func TestStatsShowSourcesAndForeignPayloads(t *testing.T) {
	s := feedSession(t, StartOptions{})
	now := time.Now()
	voice := make([]byte, 160)

	s.handlePacket(packetAt(1, 1, 0, 0, voice), now)
	s.handlePacket(packetAt(1, 2, 160, 0, voice), now)
	s.handlePacket(packetAt(2, 900, 5000, 0, voice), now) // другой источник на том же порту
	s.handlePacket(packetAt(1, 3, 320, 96, voice), now)   // чужой payload type
	s.handlePacket(packetAt(1, 4, 480, 96, voice), now)
	s.handlePacket(packetAt(1, 5, 640, 101, voice), now)

	st := s.Stop()

	if st.Sources != 2 {
		t.Errorf("Sources = %d, ожидалось 2", st.Sources)
	}

	if st.OtherPayloadPackets != 3 || st.OtherPayloadTypes != "96×2, 101×1" {
		t.Errorf("чужие payload type: %d, %q", st.OtherPayloadPackets, st.OtherPayloadTypes)
	}
}

func TestStatsOneSourceByDefault(t *testing.T) {
	s := feedSession(t, StartOptions{})
	s.handlePacket(packetAt(7, 1, 0, 0, make([]byte, 160)), time.Now())

	if st := s.Stop(); st.Sources != 1 || st.OtherPayloadTypes != "" {
		t.Errorf("Sources = %d, OtherPayloadTypes = %q", st.Sources, st.OtherPayloadTypes)
	}
}
