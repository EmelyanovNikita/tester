package scenario

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tester_mm/internal/rtp"
)

func TestParseVideoCommands(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
v = video_open(rtp_a)
w = video_open(rtp_b, offer = offer_2, codec = "VP8", pt = 100)
x = video_open()
video_start(v, answer_1, host = "10.0.0.1", file = "video.h264", fps = 30, drop_keyframes = true)
video_pli(v)
video_pli(v, count = 3, interval = 100)
st = video_stats(v)
st = video_stop(v)
`)

	if len(s.Commands) != 8 {
		t.Fatalf("команд: %d, ожидалось 8", len(s.Commands))
	}

	want := []string{"video_open", "video_open", "video_open", "video_start", "video_pli", "video_pli", "video_stats", "video_stop"}
	for i, cmd := range s.Commands {
		if !cmd.IsRTP || cmd.RTPOp != want[i] {
			t.Errorf("команда %d: %+v, ожидалась %s", i, cmd, want[i])
		}
	}

	if cmd := s.Commands[1]; len(cmd.RTPArgs) != 1 || len(cmd.RTPOpts) != 3 || cmd.RTPOpts[0] != (RTPOption{"offer", "offer_2"}) {
		t.Errorf("video_open с параметрами: %+v", cmd)
	}

	if cmd := s.Commands[5]; len(cmd.RTPOpts) != 2 || cmd.RTPOpts[1] != (RTPOption{"interval", "100"}) {
		t.Errorf("video_pli с параметрами: %+v", cmd)
	}
}

func TestParseVideoErrors(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"open без переменной", "video_open(r)\n", "нужно сохранить в переменную"},
		{"open неизвестный параметр", "v = video_open(r, fps = 30)\n", `неизвестный параметр "fps"`},
		{"start с присваиванием", "x = video_start(v, a)\n", "не возвращает значение"},
		{"start без sdp", "video_start(v)\n", "неверный синтаксис video_start"},
		{"start неизвестный параметр", "video_start(v, a, tone = 440)\n", `неизвестный параметр "tone"`},
		{"pli с присваиванием", "x = video_pli(v)\n", "не возвращает значение"},
		{"pli без сеанса", "video_pli()\n", "неверный синтаксис video_pli"},
		{"stop лишний аргумент", "video_stop(v, 1)\n", "неверный синтаксис video_stop"},
	}

	for _, c := range cases {
		_, err := NewParser(data.Methods).Parse(c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

// «MSR» для сценария: шлёт H.264 на порт тестера, на PLI отвечает IDR
func startScenarioMSR(t *testing.T, to int) (rtpPort int, pliCount *atomic.Int32) {
	t.Helper()

	var rtpConn, rtcpConn *net.UDPConn

	for port := 26000; port < 28000 && rtpConn == nil; port += 2 {
		a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if err != nil {
			continue
		}

		b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port + 1})
		if err != nil {
			a.Close()
			continue
		}

		rtpConn, rtcpConn = a, b
	}

	if rtpConn == nil {
		t.Fatal("нет свободной пары портов")
	}

	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		rtpConn.Close()
		rtcpConn.Close()
	})

	pliCount = &atomic.Int32{}
	keyRequest := &atomic.Bool{}

	go func() {
		buf := make([]byte, 1500)

		for {
			n, err := rtcpConn.Read(buf)
			if err != nil {
				return
			}

			if pli, ok := rtp.CountPLI(buf[:n]); ok && pli > 0 {
				pliCount.Add(int32(pli))
				keyRequest.Store(true)
			}
		}
	}()

	go func() {
		dst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: to}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		seq, ts := uint16(1), uint32(0)
		first := true

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			nal := byte(0x41)
			if first || keyRequest.Swap(false) {
				nal = 0x65
			}

			first = false

			packet := []byte{0x80, 0x80 | 96, byte(seq >> 8), byte(seq), byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts), 1, 2, 3, 4, nal, 0x88}
			if _, err := rtpConn.WriteToUDP(packet, dst); err != nil {
				return
			}

			seq++
			ts += 1800
		}
	}()

	return rtpConn.LocalAddr().(*net.UDPAddr).Port, pliCount
}

func TestVideoScenarioEndToEnd(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	// сначала занимаем порты, чтобы знать, куда «MSR» шлёт видео
	if err := runRTPScript(t, executor, parser, "a = rtp_open(codecs = \"PCMA\")\nv = video_open(a)\nvport = v.port\nvsdp = v.sdp\n"); err != nil {
		t.Fatal(err)
	}

	vport, _ := varValue(executor, "vport")
	vsdp, _ := varValue(executor, "vsdp")

	if !strings.Contains(vsdp, "m=audio ") || !strings.Contains(vsdp, "m=video "+vport+" RTP/AVP 96\r\n") || !strings.Contains(vsdp, "a=rtcp-fb:* nack pli") {
		t.Errorf("v.sdp должен содержать аудио и видео с rtcp-fb:\n%s", vsdp)
	}

	// сеансы предыдущего Execute закрыты, порт свободен: открываем заново на нём же через новый сценарий
	port, ok := atoi(vport)
	if !ok || !portIsFree(port) {
		t.Fatalf("порт видео %q должен освободиться после сценария", vport)
	}

	msrPort, pliCount := startScenarioMSR(t, port)

	script := fmt.Sprintf(`
v = video_open()
answer = "v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video %d RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n"
video_start(v, answer)
sleep(0.3)
before = video_stats(v)
k0 = before.keyframes
video_pli(v)
sleep(0.3)
st = video_stop(v)
k1 = st.keyframes
answered = st.pli_answered
delay = st.last_pli_to_keyframe_ms
if st.keyframes <= k0 {
    fail("нет опорного кадра после PLI")
}
`, msrPort)

	// порт для нового сеанса берётся из диапазона фабрики, «MSR» шлёт на старый: подменяем фабрику на один порт
	factory, err := rtp.NewFactory("127.0.0.1", port, port+1)
	if err != nil {
		t.Fatal(err)
	}

	executor.SetRTP(factory)

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for name, want := range map[string]string{"k0": "1", "k1": "2", "answered": "1"} {
		if got, _ := varValue(executor, name); got != want {
			t.Errorf("%s = %q, ожидалось %q", name, got, want)
		}
	}

	if delay, _ := varValue(executor, "delay"); delay == "" || strings.HasPrefix(delay, "-") {
		t.Errorf("задержка PLI → опорный кадр: %q", delay)
	}

	if pliCount.Load() != 1 {
		t.Errorf("«MSR» получил PLI: %d", pliCount.Load())
	}

	if executor.videoSessions != nil {
		t.Error("после Execute видео-сеансы должны быть сняты с учёта")
	}
}

func TestVideoExecutionErrors(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	cases := []struct {
		name, script, want string
	}{
		{"неизвестный кодек", "v = video_open(codec = \"H265\")\n", "неизвестный видеокодек"},
		{"pt вне диапазона", "v = video_open(pt = 20)\n", "96-127"},
		{"не SDP", "v = video_open(\"abc\")\n", "RTP-сеансом"},
		{"аудио вместо видео", "a = rtp_open()\nvideo_pli(a)\n", "аудио-сеанс"},
		{"pli до start", "v = video_open()\nvideo_pli(v)\n", "сначала video_start"},
		{"count вне диапазона", "v = video_open()\nvideo_pli(v, count = 0)\n", "count"},
	}

	for _, c := range cases {
		err := runRTPScript(t, executor, parser, c.script)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

// writeTestH264 — файл Annex B: SPS, PPS, IDR и 199 P-срезов (один опорный кадр на весь файл)
func writeTestH264(t *testing.T) string {
	t.Helper()

	data := []byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x21}
	for i := 0; i < 199; i++ {
		data = append(data, 0, 0, 1, 0x41, 0x9a, 0x02, 0x03)
	}

	path := filepath.Join(t.TempDir(), "video.h264")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// Плечо b шлёт видео из файла, плечо a принимает и запрашивает опорный кадр — как абоненты B и A через MSR
func TestVideoScenarioFileAndPLI(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	script := `
a = video_open()
b = video_open()
video_start(b, a.sdp, file = "` + filepath.ToSlash(writeTestH264(t)) + `", fps = 50)
video_start(a, b.sdp)
sleep(0.3)
before = video_stats(a)
k0 = before.keyframes
video_pli(a)
sleep(0.3)
rx = video_stop(a)
tx = video_stop(b)
k1 = rx.keyframes
answered = rx.pli_answered
on_pli = tx.keyframes_on_pli
pli_at_b = tx.pli_received
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for name, want := range map[string]string{"k0": "1", "k1": "2", "answered": "1", "on_pli": "1", "pli_at_b": "1"} {
		if got, _ := varValue(executor, name); got != want {
			t.Errorf("%s = %q, ожидалось %q", name, got, want)
		}
	}
}

func TestVideoStartOptionErrors(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	cases := []struct{ name, script, want string }{
		{"fps", "a = video_open()\nvideo_start(a, a.sdp, fps = 0)\n", "fps"},
		{"drop_keyframes", "a = video_open()\nvideo_start(a, a.sdp, drop_keyframes = 1)\n", "drop_keyframes"},
		{"нет файла", "a = video_open()\nvideo_start(a, a.sdp, file = \"/nonexistent/v.h264\")\n", "видеофайл"},
	}

	for _, c := range cases {
		if err := runRTPScript(t, executor, parser, c.script); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}
