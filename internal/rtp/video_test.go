package rtp

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tester_mm/internal/sdp"
)

// ===== Разбор видео =====

func TestH264HasIDR(t *testing.T) {
	stapA := func(nals ...[]byte) []byte {
		p := []byte{24}
		for _, n := range nals {
			p = append(p, byte(len(n)>>8), byte(len(n)))
			p = append(p, n...)
		}

		return p
	}

	sps := []byte{0x67, 0x42, 0xe0, 0x1f}
	pps := []byte{0x68, 0xce}
	idr := []byte{0x65, 0x88, 0x84}
	slice := []byte{0x41, 0x9a}

	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"пусто", nil, false},
		{"IDR одиночным NAL", idr, true},
		{"P-срез", slice, false},
		{"SPS отдельно", sps, false},
		{"STAP-A: SPS, PPS, IDR", stapA(sps, pps, idr), true},
		{"STAP-A: SPS, PPS", stapA(sps, pps), false},
		{"STAP-A с обрезанной длиной", []byte{24, 0, 9, 0x67}, false},
		{"FU-A начало IDR", []byte{0x7c, 0x85, 0x88}, true},
		{"FU-A середина IDR", []byte{0x7c, 0x05, 0x88}, false},
		{"FU-A начало P-среза", []byte{0x7c, 0x81, 0x9a}, false},
		{"FU-A без заголовка", []byte{0x7c}, false},
	}

	for _, c := range cases {
		if got := IsVideoKeyframe("H264", c.payload); got != c.want {
			t.Errorf("%s: %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

func TestVP8IsKeyframe(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"начало кадра, P=0", []byte{0x10, 0x00, 0x00}, true},
		{"начало кадра, P=1", []byte{0x10, 0x01, 0x00}, false},
		{"не начало кадра", []byte{0x00, 0x00}, false},
		{"не первый раздел", []byte{0x11, 0x00}, false},
		{"X, I 15 бит, L, T; P=0", []byte{0x90, 0xe0, 0x80, 0x01, 0x05, 0x40, 0x00}, true},
		{"X, I 7 бит; P=1", []byte{0x90, 0x80, 0x05, 0x01}, false},
		{"X, обрезано", []byte{0x90, 0x80}, false},
		{"пусто", nil, false},
	}

	for _, c := range cases {
		if got := IsVideoKeyframe("vp8", c.payload); got != c.want {
			t.Errorf("%s: %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

func TestBuildAndCountPLI(t *testing.T) {
	packet := BuildPLI(0x11111111, 0x22222222)

	if len(packet) != 20 {
		t.Fatalf("длина %d, ожидалось 20 (RR 8 + PLI 12)", len(packet))
	}

	if packet[1] != 201 || packet[9] != 206 || packet[8]&0x1F != 1 {
		t.Errorf("заголовки RR/PLI неверны: % x", packet)
	}

	if binary.BigEndian.Uint32(packet[16:]) != 0x22222222 {
		t.Errorf("SSRC потока в PLI неверен: % x", packet[16:])
	}

	if pli, ok := CountPLI(packet); !ok || pli != 1 {
		t.Errorf("CountPLI(RR+PLI) = %d, %v", pli, ok)
	}

	// одиночный PLI, как его шлёт pjmedia без отчёта
	if pli, ok := CountPLI(packet[8:]); !ok || pli != 1 {
		t.Errorf("CountPLI(PLI) = %d, %v", pli, ok)
	}

	if pli, ok := CountPLI(packet[:8]); !ok || pli != 0 {
		t.Errorf("CountPLI(RR) = %d, %v", pli, ok)
	}

	rtpPacket := make([]byte, 20)
	Header{PayloadType: 96, Seq: 1}.put(rtpPacket)

	if _, ok := CountPLI(rtpPacket); ok {
		t.Error("RTP-пакет не должен считаться RTCP")
	}
}

// ===== SDP =====

func TestOpenVideoSDP(t *testing.T) {
	f := newTestFactory(t)

	audio := openSession(t, f, OpenOptions{Codecs: []Codec{mustCodec(t, "PCMA")}})

	v, err := f.OpenVideo(VideoOpenOptions{Base: audio.Offer()})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	if v.Port()%2 != 0 || v.RTCPPort() != v.Port()+1 {
		t.Errorf("порты RTP/RTCP: %d/%d", v.Port(), v.RTCPPort())
	}

	desc, err := sdp.Parse(v.Offer())
	if err != nil {
		t.Fatal(err)
	}

	if a, err := desc.Audio(); err != nil || a.Port != audio.Port() {
		t.Errorf("аудио-раздел потерян: %v", err)
	}

	video, err := desc.Video()
	if err != nil {
		t.Fatal(err)
	}

	if video.Port != v.Port() || video.Codecs[DefaultVideoPT].Name != "H264" || !strings.Contains(video.Fmtp[DefaultVideoPT], "packetization-mode=1") {
		t.Errorf("m=video: %+v", video)
	}

	if !strings.Contains(v.Offer(), "a=rtcp-fb:* nack pli\r\n") {
		t.Errorf("нет a=rtcp-fb:\n%s", v.Offer())
	}
}

func TestOpenVideoAnswersOffer(t *testing.T) {
	f := newTestFactory(t)

	offer := "v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 8\r\n" +
		"m=video 4002 RTP/AVP 100 109\r\na=rtpmap:100 VP8/90000\r\na=rtpmap:109 H264/90000\r\na=fmtp:109 packetization-mode=1;profile-level-id=42801f\r\n"

	v, err := f.OpenVideo(VideoOpenOptions{Offer: offer})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	if v.PT() != 109 || !strings.Contains(v.Offer(), "a=fmtp:109 packetization-mode=1;profile-level-id=42801f\r\n") {
		t.Errorf("payload type и fmtp должны браться из предложения: pt=%d\n%s", v.PT(), v.Offer())
	}

	if _, err := f.OpenVideo(VideoOpenOptions{Offer: strings.Replace(offer, "H264", "H263", 1)}); err == nil || !strings.Contains(err.Error(), "нет кодека H264") {
		t.Errorf("ожидалась ошибка про отсутствующий кодек, получили: %v", err)
	}

	if _, err := f.OpenVideo(VideoOpenOptions{Offer: "v=0\r\nm=audio 4000 RTP/AVP 8\r\n"}); err == nil || !strings.Contains(err.Error(), "m=video") {
		t.Errorf("ожидалась ошибка про отсутствие m=video, получили: %v", err)
	}
}

func TestVideoStartErrors(t *testing.T) {
	f := newTestFactory(t)

	v, err := f.OpenVideo(VideoOpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	cases := map[string]string{
		"нет m=video":   "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 4000 RTP/AVP 0\r\n",
		"порт 0":        "v=0\r\nc=IN IP4 127.0.0.1\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n",
		"другой кодек":  "v=0\r\nc=IN IP4 127.0.0.1\r\nm=video 4000 RTP/AVP 96\r\na=rtpmap:96 VP8/90000\r\n",
		"нет адреса MM": "v=0\r\nc=IN IP4 0.0.0.0\r\nm=video 4000 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n",
	}

	for name, remote := range cases {
		if err := v.Start(VideoStartOptions{RemoteSDP: remote}); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}

	if err := v.SendPLI(1, 0); err == nil {
		t.Error("PLI до video_start должен давать ошибку")
	}
}

// ===== Сквозная проверка с «MSR» на локальных сокетах =====

// fakeMSR шлёт видео-поток и отвечает опорным кадром на каждый PLI, как MSR с включённым RTCP-FB PLI
type fakeMSR struct {
	rtp, rtcp  *net.UDPConn
	ssrc       uint32
	pliMedia   atomic.Uint32 // SSRC потока из последнего принятого PLI
	pliCount   atomic.Int32
	keyRequest atomic.Bool
	done       chan struct{}
}

// listenPairLocal занимает два соседних порта (RTP чётный, RTCP следующий) на 127.0.0.1
func listenPairLocal(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()

	for port := 24000; port < 26000; port += 2 {
		a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if err != nil {
			continue
		}

		b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port + 1})
		if err != nil {
			a.Close()
			continue
		}

		return a, b
	}

	t.Fatal("нет свободной пары портов")

	return nil, nil
}

func startFakeMSR(t *testing.T, codec string, pt uint8, to *net.UDPAddr) *fakeMSR {
	t.Helper()

	rtpConn, rtcpConn := listenPairLocal(t)
	m := &fakeMSR{rtp: rtpConn, rtcp: rtcpConn, ssrc: 0xABCDEF01, done: make(chan struct{})}

	t.Cleanup(func() {
		close(m.done)
		rtpConn.Close()
		rtcpConn.Close()
	})

	// RTCP: считаем PLI и просим «кодер» выдать опорный кадр
	go func() {
		buf := make([]byte, 1500)

		for {
			n, err := rtcpConn.Read(buf)
			if err != nil {
				return
			}

			if pli, ok := CountPLI(buf[:n]); ok && pli > 0 {
				m.pliCount.Add(int32(pli))
				m.pliMedia.Store(binary.BigEndian.Uint32(buf[n-4:]))
				m.keyRequest.Store(true)
			}
		}
	}()

	// RTP: 50 кадров/с, первый кадр опорный, дальше опорный только по запросу
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		packet := make([]byte, headerLen+3)
		seq, ts := uint16(1000), uint32(0)
		first := true

		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
			}

			key := first || m.keyRequest.Swap(false)
			first = false

			payload := videoPayload(codec, key)
			packet = packet[:headerLen]
			Header{Marker: true, PayloadType: pt, Seq: seq, Timestamp: ts, SSRC: m.ssrc}.put(packet)
			packet = append(packet, payload...)

			if _, err := rtpConn.WriteToUDP(packet, to); err != nil {
				return
			}

			seq++
			ts += 1800
		}
	}()

	return m
}

func videoPayload(codec string, key bool) []byte {
	if codec == "VP8" {
		if key {
			return []byte{0x10, 0x00, 0x00}
		}

		return []byte{0x10, 0x01, 0x00}
	}

	if key {
		return []byte{0x65, 0x88, 0x84}
	}

	return []byte{0x41, 0x9a, 0x00}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("не дождались: %s", what)
}

func TestVideoPLIEndToEnd(t *testing.T) {
	for _, codec := range []string{"H264", "VP8"} {
		t.Run(codec, func(t *testing.T) {
			f := newTestFactory(t)

			c, _ := ParseVideoCodec(codec)

			v, err := f.OpenVideo(VideoOpenOptions{Codec: c})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Stop()

			msr := startFakeMSR(t, codec, uint8(v.PT()), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: v.Port()})
			msrPort := msr.rtp.LocalAddr().(*net.UDPAddr).Port

			answer := fmt.Sprintf("v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video %d RTP/AVP %d\r\na=rtpmap:%d %s/90000\r\n", msrPort, v.PT(), v.PT(), codec)
			if err := v.Start(VideoStartOptions{RemoteSDP: answer}); err != nil {
				t.Fatal(err)
			}

			waitFor(t, "первый опорный кадр и несколько кадров", func() bool {
				st := v.Stats()
				return st.Keyframes == 1 && st.Frames >= 5
			})

			if err := v.SendPLI(1, 0); err != nil {
				t.Fatal(err)
			}

			waitFor(t, "опорный кадр в ответ на PLI", func() bool { return v.Stats().PLIAnswered == 1 })

			st := v.Stats()
			if st.Keyframes != 2 || st.PLISent != 1 || st.LastPLIToKeyframeMs < 0 || st.LastPLIToKeyframeMs > 1000 {
				t.Errorf("счётчики после PLI: %+v", st)
			}

			if msr.pliCount.Load() != 1 || msr.pliMedia.Load() != msr.ssrc {
				t.Errorf("MSR получил PLI: %d, SSRC потока %08x (ожидался %08x)", msr.pliCount.Load(), msr.pliMedia.Load(), msr.ssrc)
			}

			// MSR сам просит опорный кадр у тестера
			if _, err := msr.rtcp.WriteToUDP(BuildPLI(msr.ssrc, 0), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: v.RTCPPort()}); err != nil {
				t.Fatal(err)
			}

			waitFor(t, "PLI от MSR", func() bool { return v.Stats().PLIReceived == 1 })

			final := v.Stop()
			if final.RemoteSSRC != "0xABCDEF01" || final.LostPackets != 0 || final.OtherPayloadPackets != 0 || final.DurationSec <= 0 {
				t.Errorf("итоги: %+v", final)
			}

			if again := v.Stop(); again != final {
				t.Error("повторный Stop должен вернуть те же итоги")
			}
		})
	}
}

// Несколько PLI до ответа: задержка считается от первого, ответ засчитывается один
func TestVideoPLIBurst(t *testing.T) {
	f := newTestFactory(t)

	v, err := f.OpenVideo(VideoOpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	// «MSR», который не отвечает на PLI: только принимает RTCP
	rtpConn, rtcpConn := listenPairLocal(t)
	defer rtpConn.Close()
	defer rtcpConn.Close()

	answer := fmt.Sprintf("v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video %d RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n", rtpConn.LocalAddr().(*net.UDPAddr).Port)
	if err := v.Start(VideoStartOptions{RemoteSDP: answer}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := v.SendPLI(3, 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("3 PLI с паузой 30 мс отправлены за %v", elapsed)
	}

	received := 0
	buf := make([]byte, 1500)

	for received < 3 {
		rtcpConn.SetReadDeadline(time.Now().Add(time.Second))

		n, err := rtcpConn.Read(buf)
		if err != nil {
			t.Fatalf("принято PLI: %d из 3: %v", received, err)
		}

		pli, _ := CountPLI(buf[:n])
		received += pli
	}

	st := v.Stats()
	if st.PLISent != 3 || st.PLIAnswered != 0 || st.LastPLIToKeyframeMs != -1 || st.SinceLastKeyframeMs != -1 {
		t.Errorf("счётчики без ответа: %+v", st)
	}
}

// ===== Отправка H.264 из файла =====

// writeH264 пишет синтетический файл Annex B: frames кадров, опорный (SPS, PPS, IDR) в начале и каждые gop кадров
func writeH264(t *testing.T, frames, gop, idrSize int) string {
	t.Helper()

	var data []byte

	nal := func(startCode []byte, body ...byte) {
		data = append(data, startCode...)
		data = append(data, body...)
	}

	long, short := []byte{0, 0, 0, 1}, []byte{0, 0, 1}

	for i := 0; i < frames; i++ {
		if i%gop == 0 {
			nal(long, 0x67, 0x42, 0xe0, 0x1f, 0xda)
			nal(short, 0x68, 0xce, 0x3c, 0x80)

			idr := append([]byte{0x65, 0x88}, make([]byte, idrSize)...)
			for j := 2; j < len(idr); j++ {
				idr[j] = 0x11
			}

			nal(long, idr...)

			continue
		}

		slice := append([]byte{0x41, 0x9a}, make([]byte, 100)...)
		for j := 2; j < len(slice); j++ {
			slice[j] = 0x22
		}

		nal(short, slice...)
	}

	path := t.TempDir() + "/video.h264"
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestParseH264Stream(t *testing.T) {
	stream, err := loadH264(writeH264(t, 25, 10, 50))
	if err != nil {
		t.Fatal(err)
	}

	if len(stream.aus) != 25 {
		t.Fatalf("кадров %d, ожидалось 25", len(stream.aus))
	}

	if fmt.Sprint(stream.keys) != "[0 10 20]" {
		t.Errorf("опорные кадры: %v", stream.keys)
	}

	if first := stream.aus[0]; len(first.nals) != 3 || first.nals[0][0] != 0x67 || first.nals[2][0] != 0x65 {
		t.Errorf("первый кадр должен быть SPS, PPS, IDR: %d NAL", len(first.nals))
	}

	if stream.nextKey(11) != 20 || stream.nextKey(21) != 0 || stream.nextKey(10) != 10 {
		t.Errorf("nextKey: %d %d %d", stream.nextKey(11), stream.nextKey(21), stream.nextKey(10))
	}

	if _, err := parseH264Stream([]byte("not a video")); err == nil {
		t.Error("ожидалась ошибка: нет стартовых кодов")
	}

	if _, err := parseH264Stream([]byte{0, 0, 1, 0x41, 0x9a, 0x01, 0, 0, 1, 0x41, 0x9a, 0x02}); err == nil || !strings.Contains(err.Error(), "IDR") {
		t.Errorf("ожидалась ошибка про отсутствие IDR: %v", err)
	}

	if _, err := loadH264(t.TempDir() + "/nope.h264"); err == nil {
		t.Error("ожидалась ошибка: файла нет")
	}
}

func TestPacketizeH264(t *testing.T) {
	small := []byte{0x67, 1, 2, 3}
	if p := packetizeH264(small, maxVideoPayload); len(p) != 1 || &p[0][0] != &small[0] {
		t.Errorf("маленький NAL должен уйти одним пакетом без копирования")
	}

	nal := append([]byte{0x65}, make([]byte, 3000)...)
	for i := 1; i < len(nal); i++ {
		nal[i] = byte(i)
	}

	payloads := packetizeH264(nal, maxVideoPayload)
	if len(payloads) != 3 {
		t.Fatalf("фрагментов %d, ожидалось 3", len(payloads))
	}

	rebuilt := []byte{payloads[0][0]&0xE0 | payloads[0][1]&0x1F}

	for i, p := range payloads {
		if len(p) > maxVideoPayload || p[0]&0x1F != 28 {
			t.Errorf("фрагмент %d: длина %d, тип %d", i, len(p), p[0]&0x1F)
		}

		if start, end := p[1]&0x80 != 0, p[1]&0x40 != 0; start != (i == 0) || end != (i == len(payloads)-1) {
			t.Errorf("фрагмент %d: S=%v E=%v", i, start, end)
		}

		rebuilt = append(rebuilt, p[2:]...)
	}

	if string(rebuilt) != string(nal) {
		t.Error("из фрагментов не собирается исходный NAL")
	}

	if !IsVideoKeyframe("H264", payloads[0]) || IsVideoKeyframe("H264", payloads[1]) {
		t.Error("опорным должен считаться только первый фрагмент IDR")
	}
}

// pairVideo открывает два видео-сеанса и запускает их друг на друга: sender шлёт файл, receiver только принимает
func pairVideo(t *testing.T, start VideoStartOptions) (sender, receiver *VideoSession) {
	t.Helper()

	f := newTestFactory(t)

	var err error

	if sender, err = f.OpenVideo(VideoOpenOptions{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { sender.Stop() })

	if receiver, err = f.OpenVideo(VideoOpenOptions{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { receiver.Stop() })

	start.RemoteSDP = receiver.Offer()
	if err := sender.Start(start); err != nil {
		t.Fatal(err)
	}

	if err := receiver.Start(VideoStartOptions{RemoteSDP: sender.Offer()}); err != nil {
		t.Fatal(err)
	}

	return sender, receiver
}

func TestVideoSendFromFileAndAnswerPLI(t *testing.T) {
	// один опорный кадр на весь файл: 300 кадров при 50 кадр/с — 6 с, тест короче
	sender, receiver := pairVideo(t, VideoStartOptions{File: writeH264(t, 300, 1000, 3000), FPS: 50})

	waitFor(t, "кадры и первый опорный кадр", func() bool {
		st := receiver.Stats()
		return st.Frames >= 10 && st.Keyframes == 1
	})

	if err := receiver.SendPLI(1, 0); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "опорный кадр в ответ на PLI", func() bool { return receiver.Stats().PLIAnswered == 1 })

	rx := receiver.Stop()
	tx := sender.Stop()

	if rx.Keyframes != 2 || rx.LostPackets != 0 || rx.LastPLIToKeyframeMs < 0 || rx.LastPLIToKeyframeMs > 200 {
		t.Errorf("приём: %+v", rx)
	}

	if tx.PLIReceived != 1 || tx.KeyframesOnPLI != 1 || tx.SentKeyframes != 2 || tx.SentFrames < rx.Frames || tx.DroppedKeyframes != 0 {
		t.Errorf("отправка: %+v", tx)
	}

	// IDR в 3000 байт уходит фрагментами: пакетов больше, чем кадров
	if tx.SentPackets <= tx.SentFrames {
		t.Errorf("пакетов %d, кадров %d", tx.SentPackets, tx.SentFrames)
	}
}

func TestVideoDropKeyframes(t *testing.T) {
	sender, receiver := pairVideo(t, VideoStartOptions{File: writeH264(t, 300, 1000, 100), FPS: 50, DropKeyframes: true})

	waitFor(t, "кадры без опорного", func() bool { return receiver.Stats().Frames >= 10 })

	if st := receiver.Stats(); st.Keyframes != 0 {
		t.Fatalf("при drop_keyframes опорных кадров быть не должно: %+v", st)
	}

	if err := receiver.SendPLI(1, 0); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "опорный кадр в ответ на PLI", func() bool { return receiver.Stats().Keyframes == 1 })

	tx := sender.Stop()
	if tx.DroppedKeyframes != 1 || tx.KeyframesOnPLI != 1 || tx.SentKeyframes != 1 {
		t.Errorf("отправка: %+v", tx)
	}
}

func TestVideoStartFileErrors(t *testing.T) {
	f := newTestFactory(t)

	remote := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=video 4000 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n"

	cases := map[string]VideoStartOptions{
		"нет файла":         {File: t.TempDir() + "/nope.h264"},
		"drop без файла":    {DropKeyframes: true},
		"fps вне диапазона": {File: writeH264(t, 5, 5, 10), FPS: 100},
		"не H.264 в файле":  {File: writeFile(t, "garbage")},
	}

	for name, opts := range cases {
		v, err := f.OpenVideo(VideoOpenOptions{})
		if err != nil {
			t.Fatal(err)
		}

		opts.RemoteSDP = remote
		if err := v.Start(opts); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}

		v.Stop()
	}

	vp8, _ := ParseVideoCodec("VP8")

	v, err := f.OpenVideo(VideoOpenOptions{Codec: vp8})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Stop()

	vp8Remote := strings.Replace(remote, "H264", "VP8", 1)
	if err := v.Start(VideoStartOptions{RemoteSDP: vp8Remote, File: writeH264(t, 5, 5, 10)}); err == nil || !strings.Contains(err.Error(), "только для H264") {
		t.Errorf("VP8 из файла: %v", err)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()

	path := t.TempDir() + "/file.bin"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}
