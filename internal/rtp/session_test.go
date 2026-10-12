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

// Диапазон портов этого пакета; пакет scenario использует другой, чтобы тесты, идущие параллельно, не мешали друг другу
const (
	testPortMin = 20000
	testPortMax = 21999
)

func newTestFactory(t *testing.T) *Factory {
	t.Helper()

	f, err := NewFactory("127.0.0.1", testPortMin, testPortMax)
	if err != nil {
		t.Fatal(err)
	}

	return f
}

func openSession(t *testing.T, f *Factory, opts OpenOptions) *Session {
	t.Helper()

	s, err := f.Open(opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { s.Stop() })

	return s
}

// answerSDP собирает ответ «удалённой стороны» с заданным портом, кодеками и дополнительными строками
func answerSDP(port int, formats string, extra ...string) string {
	lines := []string{"v=0", "o=- 1 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0", fmt.Sprintf("m=audio %d RTP/AVP %s", port, formats)}
	lines = append(lines, extra...)

	return strings.Join(lines, "\r\n") + "\r\n"
}

// listener — «удалённая сторона»: сырой UDP-сокет, на который тестер шлёт RTP
func listener(t *testing.T) *net.UDPConn {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { conn.Close() })

	return conn
}

func portOf(conn *net.UDPConn) int { return conn.LocalAddr().(*net.UDPAddr).Port }

// packetAt собирает RTP-пакет с данной нагрузкой
func packetAt(ssrc uint32, seq uint16, ts uint32, pt uint8, payload []byte) []byte {
	b := make([]byte, headerLen+len(payload))
	Header{PayloadType: pt, Seq: seq, Timestamp: ts, SSRC: ssrc}.put(b)
	copy(b[headerLen:], payload)

	return b
}

func readSamples(t *testing.T, path string) []int16 {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) < wavHeaderLen || string(data[0:4]) != "RIFF" {
		t.Fatalf("%s: это не WAV (%d байт)", path, len(data))
	}

	samples := make([]int16, (len(data)-wavHeaderLen)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[wavHeaderLen+2*i:]))
	}

	return samples
}

// ===== Фабрика =====

func TestNewFactoryValidation(t *testing.T) {
	cases := []struct {
		ip       string
		min, max int
	}{
		{"не-адрес", 20000, 21000},
		{"127.0.0.1", 0, 100},
		{"127.0.0.1", 100, 70000},
		{"127.0.0.1", 200, 100},
	}

	for _, c := range cases {
		if _, err := NewFactory(c.ip, c.min, c.max); err == nil {
			t.Errorf("NewFactory(%q, %d, %d): ожидалась ошибка", c.ip, c.min, c.max)
		}
	}
}

func TestOpenAllocatesEvenPortsInRange(t *testing.T) {
	f := newTestFactory(t)

	for i := 0; i < 5; i++ {
		s := openSession(t, f, OpenOptions{})

		if s.Port() < testPortMin || s.Port() > testPortMax || s.Port()%2 != 0 {
			t.Errorf("порт %d: ожидался чётный из %d-%d", s.Port(), testPortMin, testPortMax)
		}
	}
}

func TestOpenExhaustionAndReuse(t *testing.T) {
	f, err := NewFactory("127.0.0.1", 20500, 20501) // один чётный порт
	if err != nil {
		t.Fatal(err)
	}

	first := openSession(t, f, OpenOptions{})

	if _, err := f.Open(OpenOptions{}); err == nil || !strings.Contains(err.Error(), "нет свободных портов") {
		t.Fatalf("ожидалась ошибка нехватки портов, получили: %v", err)
	}

	port := first.Port()
	first.Stop()

	again := openSession(t, f, OpenOptions{})
	if again.Port() != port {
		t.Errorf("после Stop порт %d должен освободиться, получили %d", port, again.Port())
	}
}

func TestOpenFixedPortBusy(t *testing.T) {
	f := newTestFactory(t)

	first := openSession(t, f, OpenOptions{})

	if _, err := f.Open(OpenOptions{Port: first.Port()}); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("ожидалась ошибка занятого порта, получили: %v", err)
	}
}

func TestOpenValidation(t *testing.T) {
	f := newTestFactory(t)

	if _, err := f.Open(OpenOptions{IP: "bad"}); err == nil {
		t.Error("неверный ip должен давать ошибку")
	}

	if _, err := f.Open(OpenOptions{PTime: 500}); err == nil {
		t.Error("ptime 500 должен давать ошибку")
	}
}

func TestOpenConcurrentGivesDistinctPorts(t *testing.T) {
	f := newTestFactory(t)

	const n = 50

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ports = map[int]bool{}
	)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			s, err := f.Open(OpenOptions{})
			if err != nil {
				t.Error(err)
				return
			}

			defer s.Stop()

			mu.Lock()
			defer mu.Unlock()

			if ports[s.Port()] {
				t.Errorf("порт %d выдан дважды", s.Port())
			}

			ports[s.Port()] = true
		}()
	}

	wg.Wait()

	if len(ports) != n {
		t.Errorf("получено %d портов, ожидалось %d", len(ports), n)
	}
}

func TestOfferContainsPortAndCodecs(t *testing.T) {
	f := newTestFactory(t)
	s := openSession(t, f, OpenOptions{Codecs: []Codec{PCMA}, PTime: 30})

	offer := s.Offer()
	for _, want := range []string{
		fmt.Sprintf("m=audio %d RTP/AVP 8\r\n", s.Port()),
		"a=rtpmap:8 PCMA/8000\r\n",
		"a=ptime:30\r\n",
		"c=IN IP4 127.0.0.1\r\n",
	} {
		if !strings.Contains(offer, want) {
			t.Errorf("в offer нет %q:\n%s", want, offer)
		}
	}

	custom := openSession(t, f, OpenOptions{Offer: "мой sdp"})
	if custom.Offer() != "мой sdp" {
		t.Errorf("готовый offer должен возвращаться как есть, получили %q", custom.Offer())
	}
}

// ===== Запуск =====

func TestStartErrors(t *testing.T) {
	f := newTestFactory(t)

	cases := []struct {
		name string
		sdp  string
		want string
	}{
		{"нет audio", "v=0\r\n", "нет раздела m=audio"},
		{"порт 0", answerSDP(0, "0"), "отклонил"},
		{"нет кодека", answerSDP(4000, "97 111", "a=rtpmap:97 opus/48000/2", "a=rtpmap:111 telephone-event/8000"), "нет поддерживаемого кодека"},
		{"неверный m=", "m=audio x RTP/AVP 0\r\n", "неверный порт"},
		{"частота не та", answerSDP(4000, "96", "a=rtpmap:96 PCMU/16000"), "нет поддерживаемого кодека"},
	}

	for _, c := range cases {
		s := openSession(t, f, OpenOptions{})

		err := s.Start(StartOptions{RemoteSDP: c.sdp})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}

		// после неудачного запуска сеанс остаётся пригодным
		if s.Stats().SentPackets != 0 {
			t.Errorf("%s: пакеты не должны идти", c.name)
		}
	}
}

func TestStartHostSelection(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	hold := "v=0\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\n" + fmt.Sprintf("m=audio %d RTP/AVP 0\r\n", portOf(remote))

	// 0.0.0.0 в SDP и нет запасного адреса
	s := openSession(t, f, OpenOptions{})
	if err := s.Start(StartOptions{RemoteSDP: hold}); err != ErrHostRequired {
		t.Fatalf("ожидалась ErrHostRequired, получили: %v", err)
	}

	// запасной адрес (адрес MM) подставляется вместо 0.0.0.0
	if err := s.Start(StartOptions{RemoteSDP: hold, DefaultHost: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}

	if got := s.Stats().Remote; got != fmt.Sprintf("127.0.0.1:%d", portOf(remote)) {
		t.Errorf("remote = %s", got)
	}

	// принудительный адрес главнее SDP
	forced := openSession(t, f, OpenOptions{})
	if err := forced.Start(StartOptions{RemoteSDP: answerSDP(1234, "0"), Host: "127.0.0.2"}); err != nil {
		t.Fatal(err)
	}

	if got := forced.Stats().Remote; got != "127.0.0.2:1234" {
		t.Errorf("remote = %s, ожидался принудительный адрес", got)
	}
}

func TestStartTwiceAndAfterStop(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	s := openSession(t, f, OpenOptions{})
	sdpText := answerSDP(portOf(remote), "0")

	if err := s.Start(StartOptions{RemoteSDP: sdpText}); err != nil {
		t.Fatal(err)
	}

	if err := s.Start(StartOptions{RemoteSDP: sdpText}); err == nil || !strings.Contains(err.Error(), "уже запущен") {
		t.Errorf("повторный Start: %v", err)
	}

	s.Stop()

	if err := s.Start(StartOptions{RemoteSDP: sdpText}); err == nil || !strings.Contains(err.Error(), "остановлен") {
		t.Errorf("Start после Stop: %v", err)
	}
}

// ===== Отправка: формат пакетов =====

func TestSendFormat(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	s := openSession(t, f, OpenOptions{})

	// в ответе кодек под номером 96 и ptime 10: тестер должен использовать именно их
	sdpText := answerSDP(portOf(remote), "96", "a=rtpmap:96 PCMA/8000", "a=ptime:10")
	if err := s.Start(StartOptions{RemoteSDP: sdpText, ToneHz: 440}); err != nil {
		t.Fatal(err)
	}

	var packets []Header

	buf := make([]byte, 2048)

	for len(packets) < 8 {
		remote.SetReadDeadline(time.Now().Add(2 * time.Second))

		n, _, err := remote.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("пакет не пришёл: %v", err)
		}

		header, payload, err := ParsePacket(buf[:n])
		if err != nil {
			t.Fatal(err)
		}

		if len(payload) != 80 {
			t.Fatalf("при ptime 10 и 8000 Гц в пакете 80 отсчётов, получили %d", len(payload))
		}

		packets = append(packets, header)
	}

	if !packets[0].Marker || packets[1].Marker {
		t.Error("marker должен стоять только на первом пакете")
	}

	for i, h := range packets {
		if h.PayloadType != 96 || h.SSRC != packets[0].SSRC {
			t.Errorf("пакет %d: pt=%d ssrc=%d", i, h.PayloadType, h.SSRC)
		}

		if i > 0 && (h.Seq != packets[i-1].Seq+1 || h.Timestamp != packets[i-1].Timestamp+80) {
			t.Errorf("пакет %d: seq=%d ts=%d после seq=%d ts=%d", i, h.Seq, h.Timestamp, packets[i-1].Seq, packets[i-1].Timestamp)
		}
	}
}

func TestSendPacing(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	s := openSession(t, f, OpenOptions{PTime: 20})
	if err := s.Start(StartOptions{RemoteSDP: answerSDP(portOf(remote), "0")}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1050 * time.Millisecond)

	stats := s.Stop()

	// 50 пакетов в секунду; допускаем разброс планировщика
	if stats.SentPackets < 45 || stats.SentPackets > 56 {
		t.Errorf("за ~1 с при ptime 20 ожидалось около 50 пакетов, отправлено %d", stats.SentPackets)
	}

	if stats.SentBytes != stats.SentPackets*(headerLen+160) {
		t.Errorf("байт: %d на %d пакетов", stats.SentBytes, stats.SentPackets)
	}

	if stats.Codec != "PCMU" {
		t.Errorf("codec = %q", stats.Codec)
	}
}

func TestSendDirection(t *testing.T) {
	f := newTestFactory(t)

	cases := []struct {
		name  string
		extra string
		opts  StartOptions
		send  bool
	}{
		{"sendrecv", "a=sendrecv", StartOptions{}, true},
		{"recvonly у MM", "a=recvonly", StartOptions{}, true},
		{"sendonly у MM", "a=sendonly", StartOptions{}, false},
		{"inactive", "a=inactive", StartOptions{}, false},
		{"send=false", "a=sendrecv", StartOptions{NoSend: true}, false},
	}

	for _, c := range cases {
		remote := listener(t)
		s := openSession(t, f, OpenOptions{})

		c.opts.RemoteSDP = answerSDP(portOf(remote), "0", c.extra)
		if err := s.Start(c.opts); err != nil {
			t.Fatal(err)
		}

		remote.SetReadDeadline(time.Now().Add(150 * time.Millisecond))

		_, _, err := remote.ReadFromUDP(make([]byte, 2048))
		got := err == nil

		if got != c.send {
			t.Errorf("%s: пакеты идут = %v, ожидалось %v", c.name, got, c.send)
		}

		s.Stop()
	}
}

// ===== Приём: счётчики =====

// feedSession запускает сеанс без отправки и возвращает функцию, подающую ему пакеты напрямую
func feedSession(t *testing.T, opts StartOptions) *Session {
	t.Helper()

	f := newTestFactory(t)
	s := openSession(t, f, OpenOptions{})

	opts.RemoteSDP = answerSDP(4000, "0")
	opts.NoSend = true

	if err := s.Start(opts); err != nil {
		t.Fatal(err)
	}

	return s
}

var pcmuSilence = make([]byte, 160)

func TestReceiveCounters(t *testing.T) {
	cases := []struct {
		name string
		seqs []uint16
		recv uint64
		lost uint64
		ooo  uint64
		dup  uint64
	}{
		{"по порядку", []uint16{10, 11, 12, 13}, 4, 0, 0, 0},
		{"потеря", []uint16{1, 2, 5}, 3, 2, 0, 0},
		{"перестановка", []uint16{100, 101, 103, 102, 104}, 5, 0, 1, 0},
		{"дубликат", []uint16{7, 8, 8, 9}, 3, 0, 0, 1},
		{"переход через 65535", []uint16{65534, 65535, 0, 1}, 4, 0, 0, 0},
		{"потеря на переходе", []uint16{65534, 1}, 2, 2, 0, 0},
	}

	for _, c := range cases {
		s := feedSession(t, StartOptions{})
		now := time.Now()

		for i, seq := range c.seqs {
			s.handlePacket(packetAt(1, seq, uint32(i)*160, 0, pcmuSilence), now.Add(time.Duration(i)*20*time.Millisecond))
		}

		st := s.Stats()
		if st.ReceivedPackets != c.recv || st.LostPackets != c.lost || st.OutOfOrder != c.ooo || st.Duplicates != c.dup {
			t.Errorf("%s: принято %d потеряно %d не по порядку %d дублей %d; ожидалось %d/%d/%d/%d",
				c.name, st.ReceivedPackets, st.LostPackets, st.OutOfOrder, st.Duplicates, c.recv, c.lost, c.ooo, c.dup)
		}
	}
}

func TestReceiveLossPercent(t *testing.T) {
	s := feedSession(t, StartOptions{})
	now := time.Now()

	// из 10 номеров доходят 8
	for _, seq := range []uint16{0, 1, 2, 3, 5, 6, 8, 9} {
		s.handlePacket(packetAt(1, seq, uint32(seq)*160, 0, pcmuSilence), now)
	}

	st := s.Stats()
	if st.LostPackets != 2 || math.Abs(st.LossPercent-20) > 1e-9 {
		t.Errorf("потеряно %d (%v%%), ожидалось 2 (20%%)", st.LostPackets, st.LossPercent)
	}
}

func TestReceiveOtherPayloadAndBadPackets(t *testing.T) {
	s := feedSession(t, StartOptions{})
	now := time.Now()

	s.handlePacket(packetAt(1, 1, 0, 101, []byte{1, 2, 3, 4}), now) // DTMF
	s.handlePacket([]byte{1, 2, 3}, now)                            // мусор
	s.handlePacket(packetAt(1, 2, 0, 0, pcmuSilence), now)

	st := s.Stats()
	if st.OtherPayloadPackets != 1 || st.BadPackets != 1 || st.ReceivedPackets != 1 {
		t.Errorf("other=%d bad=%d received=%d, ожидалось 1/1/1", st.OtherPayloadPackets, st.BadPackets, st.ReceivedPackets)
	}
}

func TestReceiveSourceRestart(t *testing.T) {
	s := feedSession(t, StartOptions{})
	now := time.Now()

	// первый источник: потеряны 2 пакета
	for _, seq := range []uint16{1, 2, 5} {
		s.handlePacket(packetAt(1, seq, 0, 0, pcmuSilence), now)
	}

	// новый SSRC: учёт начинается заново, потери первого сохраняются
	for _, seq := range []uint16{100, 101} {
		s.handlePacket(packetAt(2, seq, 0, 0, pcmuSilence), now)
	}

	// тот же SSRC, но огромный скачок номера: тоже новый источник
	s.handlePacket(packetAt(2, 30000, 0, 0, pcmuSilence), now)

	st := s.Stats()
	if st.ReceivedPackets != 6 || st.LostPackets != 2 {
		t.Errorf("принято %d потеряно %d, ожидалось 6 и 2", st.ReceivedPackets, st.LostPackets)
	}
}

func TestReceiveJitter(t *testing.T) {
	s := feedSession(t, StartOptions{})
	base := time.Now()

	// ровный поток: джиттер нулевой
	for i := 0; i < 5; i++ {
		s.handlePacket(packetAt(1, uint16(i), uint32(i)*160, 0, pcmuSilence), base.Add(time.Duration(i)*20*time.Millisecond))
	}

	if j := s.Stats().JitterMs; j > 1e-6 {
		t.Fatalf("ровный поток дал джиттер %v мс", j)
	}

	// пакет опоздал на 10 мс = 80 отсчётов: джиттер += 80/16 = 5 отсчётов = 0.625 мс
	s.handlePacket(packetAt(1, 5, 5*160, 0, pcmuSilence), base.Add(5*20*time.Millisecond+10*time.Millisecond))

	if j := s.Stats().JitterMs; math.Abs(j-0.625) > 1e-6 {
		t.Errorf("джиттер = %v мс, ожидалось 0.625", j)
	}
}

func TestRecordWithGapFill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec-{id}.wav")
	s := feedSession(t, StartOptions{Record: path})
	now := time.Now()

	tonePayload := make([]byte, 160)
	for i := range tonePayload {
		tonePayload[i] = encodeULaw(5000)
	}

	s.handlePacket(packetAt(1, 1, 0, 0, tonePayload), now)
	s.handlePacket(packetAt(1, 3, 320, 0, tonePayload), now) // пакет с ts=160 потерян
	s.handlePacket(packetAt(1, 2, 160, 0, tonePayload), now) // запоздал: место уже занято тишиной

	s.Stop()

	if err := s.RecordError(); err != nil {
		t.Fatal(err)
	}

	samples := readSamples(t, strings.ReplaceAll(path, "{id}", s.ID()))
	if len(samples) != 480 {
		t.Fatalf("в записи %d отсчётов, ожидалось 480 (звук, тишина вместо потерянного, звук)", len(samples))
	}

	if samples[0] < 4000 || samples[200] != 0 || samples[400] < 4000 {
		t.Errorf("запись: начало %d, середина %d, конец %d", samples[0], samples[200], samples[400])
	}
}

func TestStopIsIdempotentAndFreesPort(t *testing.T) {
	f := newTestFactory(t)
	remote := listener(t)

	s := openSession(t, f, OpenOptions{})
	port := s.Port()

	if err := s.Start(StartOptions{RemoteSDP: answerSDP(portOf(remote), "0")}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(60 * time.Millisecond)

	first := s.Stop()
	second := s.Stop()

	if first != second {
		t.Errorf("повторный Stop вернул другие значения:\n%+v\n%+v", first, second)
	}

	if s.Stats() != first {
		t.Error("Stats после Stop должны быть итоговыми")
	}

	if first.SentPackets == 0 {
		t.Error("за 60 мс должен уйти хотя бы один пакет")
	}

	conn, err := listenUDP("127.0.0.1", port)
	if err != nil {
		t.Fatalf("порт %d должен освободиться: %v", port, err)
	}

	conn.Close()
}

func TestStopWithoutStart(t *testing.T) {
	s := openSession(t, newTestFactory(t), OpenOptions{})

	st := s.Stop()
	if st.SentPackets != 0 || st.ReceivedPackets != 0 || st.DurationSec != 0 {
		t.Errorf("остановка без запуска должна давать нули: %+v", st)
	}
}

// ===== Два сеанса друг с другом =====

func TestLoopbackBothDirections(t *testing.T) {
	f := newTestFactory(t)
	dir := t.TempDir()

	// обе стороны предлагают только PCMA, как отвечает MM: один кодек на оба направления
	a := openSession(t, f, OpenOptions{Codecs: []Codec{PCMA}})
	b := openSession(t, f, OpenOptions{Codecs: []Codec{PCMA}})

	recA := filepath.Join(dir, "a.wav")
	recB := filepath.Join(dir, "b.wav")

	if err := a.Start(StartOptions{RemoteSDP: b.Offer(), ToneHz: 440, Record: recA}); err != nil {
		t.Fatal(err)
	}

	if err := b.Start(StartOptions{RemoteSDP: a.Offer(), ToneHz: 1000, Record: recB}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(600 * time.Millisecond)

	statsA := a.Stop()
	statsB := b.Stop()

	for name, st := range map[string]Stats{"A": statsA, "B": statsB} {
		if st.ReceivedPackets < 15 || st.SentPackets < 15 {
			t.Errorf("%s: отправлено %d, принято %d: ожидалось не меньше 15", name, st.SentPackets, st.ReceivedPackets)
		}

		if st.LostPackets > 1 {
			t.Errorf("%s: потеряно %d на loopback", name, st.LostPackets)
		}

		if st.Codec != "PCMA" {
			t.Errorf("%s: кодек %s, ожидался PCMA", name, st.Codec)
		}
	}

	if a.RecordError() != nil || b.RecordError() != nil {
		t.Fatalf("ошибки записи: %v %v", a.RecordError(), b.RecordError())
	}

	// A слышит тон B (1000 Гц), B слышит тон A (440 Гц)
	heardByA := readSamples(t, recA)
	heardByB := readSamples(t, recB)

	if len(heardByA) < 2000 || len(heardByB) < 2000 {
		t.Fatalf("записи слишком короткие: %d и %d отсчётов", len(heardByA), len(heardByB))
	}

	if goertzel(heardByA, 1000, 8000) < goertzel(heardByA, 440, 8000)*100 {
		t.Error("в записи A должен слышаться тон 1000 Гц")
	}

	if goertzel(heardByB, 440, 8000) < goertzel(heardByB, 1000, 8000)*100 {
		t.Error("в записи B должен слышаться тон 440 Гц")
	}
}

// Если MM шлёт не тот кодек, который согласован в SDP, это видно в счётчиках, а не теряется молча
func TestLoopbackCodecMismatchIsVisible(t *testing.T) {
	f := newTestFactory(t)

	a := openSession(t, f, OpenOptions{Codecs: []Codec{PCMA}})
	b := openSession(t, f, OpenOptions{})

	// B выбирает PCMU (первый в предложении по умолчанию), A шлёт PCMA
	if err := a.Start(StartOptions{RemoteSDP: b.Offer()}); err != nil {
		t.Fatal(err)
	}

	if err := b.Start(StartOptions{RemoteSDP: a.Offer()}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	statsB := b.Stop()
	a.Stop()

	if statsB.ReceivedPackets != 0 || statsB.OtherPayloadPackets == 0 {
		t.Errorf("B: принято %d, с другим payload type %d: ожидалось 0 и больше 0", statsB.ReceivedPackets, statsB.OtherPayloadPackets)
	}
}
