package scenario

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tester_mm/internal/rtp"
)

// ===== Разбор =====

func TestParseRTPCommands(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
rtp = rtp_open()
a = rtp_open(sdp_offer)
b = rtp_open(codecs = "PCMA,PCMU", ptime = 10, ip = "127.0.0.1", port = 4000)
rtp_start(rtp, chan.sdp_answer, tone = 880, record = "in_{id}.wav", send = false, host = "10.0.0.1")
st = rtp_stats(rtp)
rtp_stats(rtp)
rtp_stop(rtp)
st = rtp_stop(rtp)
c = rtp_open("v=0\r\nm=audio 4000 RTP/AVP 0\r\n")
rtp_start(rtp, x, record = "a=b.wav")
`)

	if len(s.Commands) != 10 {
		t.Fatalf("команд: %d, ожидалось 10", len(s.Commands))
	}

	open := s.Commands[0]
	if !open.IsRTP || open.RTPOp != "rtp_open" || open.VarName != "rtp" || len(open.RTPArgs) != 0 || len(open.RTPOpts) != 0 {
		t.Errorf("rtp_open(): %+v", open)
	}

	if cmd := s.Commands[1]; len(cmd.RTPArgs) != 1 || cmd.RTPArgs[0] != "sdp_offer" {
		t.Errorf("rtp_open(sdp_offer): %+v", cmd)
	}

	if cmd := s.Commands[2]; len(cmd.RTPOpts) != 4 || cmd.RTPOpts[0] != (RTPOption{"codecs", `"PCMA,PCMU"`}) || cmd.RTPOpts[3] != (RTPOption{"port", "4000"}) {
		t.Errorf("rtp_open с параметрами: %+v", cmd.RTPOpts)
	}

	start := s.Commands[3]
	if start.RTPOp != "rtp_start" || start.VarName != "" || len(start.RTPArgs) != 2 || start.RTPArgs[1] != "chan.sdp_answer" || len(start.RTPOpts) != 4 {
		t.Errorf("rtp_start: %+v", start)
	}

	if cmd := s.Commands[4]; cmd.RTPOp != "rtp_stats" || cmd.VarName != "st" {
		t.Errorf("st = rtp_stats: %+v", cmd)
	}

	if cmd := s.Commands[5]; cmd.RTPOp != "rtp_stats" || cmd.VarName != "" {
		t.Errorf("rtp_stats без переменной: %+v", cmd)
	}

	if cmd := s.Commands[7]; cmd.RTPOp != "rtp_stop" || cmd.VarName != "st" {
		t.Errorf("st = rtp_stop: %+v", cmd)
	}

	// знак = и запятые внутри кавычек не мешают разбору
	if cmd := s.Commands[8]; len(cmd.RTPArgs) != 1 || !strings.HasPrefix(cmd.RTPArgs[0], `"v=0`) {
		t.Errorf("строка с = в позиционном аргументе: %+v", cmd.RTPArgs)
	}

	if cmd := s.Commands[9]; cmd.RTPOpts[0].Value != `"a=b.wav"` {
		t.Errorf("значение с = в кавычках: %+v", cmd.RTPOpts)
	}
}

func TestParseRTPInsideBlocks(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "for i in 1..2 {\n    r = rtp_open()\n    rtp_stop(r)\n}\nif x == 1 {\n    rtp_stop(r)\n}\n")

	if len(s.Commands) != 2 || len(s.Commands[0].Body) != 2 || !s.Commands[0].Body[0].IsRTP || !s.Commands[1].Body[0].IsRTP {
		t.Errorf("RTP-команды внутри for и if не разобраны: %+v", s.Commands)
	}
}

func TestParseRTPErrors(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"не закрыта скобка", "r = rtp_open(\n", "неверный синтаксис rtp_open"},
		{"неизвестный параметр", "r = rtp_open(speed = 5)\n", `неизвестный параметр "speed"`},
		{"параметр дважды", "r = rtp_open(ptime = 10, ptime = 20)\n", "указан дважды"},
		{"start без sdp", "rtp_start(r)\n", "неверный синтаксис rtp_start"},
		{"start лишний аргумент", "rtp_start(r, a, b)\n", "неверный синтаксис rtp_start"},
		{"stop без аргумента", "rtp_stop()\n", "неверный синтаксис rtp_stop"},
		{"stats параметр", "rtp_stats(r, tone = 1)\n", "именованных аргументов нет"},
		{"сравнение", "rtp_start(r, a, tone == 1)\n", "сравнение"},
		{"пустой аргумент", "rtp_start(r, , tone = 1)\n", "пустой аргумент"},
		{"нет значения", "rtp_start(r, a, tone = )\n", "нет значения"},
		{"start с присваиванием", "x = rtp_start(r, a)\n", "не возвращает значение"},
		{"open без переменной", "rtp_open()\n", "нужно сохранить в переменную"},
		{"слева не имя", "rtp_start(r, a, 5 = 1)\n", "имя параметра"},
		{"dtmf с присваиванием", "x = rtp_dtmf(r, \"1\")\n", "не возвращает значение"},
		{"dtmf неизвестный параметр", "rtp_dtmf(r, \"1\", len = 5)\n", `неизвестный параметр "len"`},
		{"dtmf без цифр", "rtp_dtmf(r)\n", "неверный синтаксис rtp_dtmf"},
	}

	for _, c := range cases {
		_, err := NewParser(data.Methods).Parse(c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "строка 1") {
			t.Errorf("%s: ожидалась ошибка со «строка 1» и %q, получили: %v", c.name, c.want, err)
		}
	}
}

// Переменные, чьи имена начинаются как RTP-команды, остаются обычными переменными
func TestRTPKeywordsAsVariables(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "rtp_open = 5\nrtp_stats_total = 7\nrtp_stop = rtp_open\n")

	if len(s.Commands) != 3 {
		t.Fatalf("команд: %d", len(s.Commands))
	}

	for i, cmd := range s.Commands {
		if cmd.IsRTP || !cmd.IsSimpleAssign {
			t.Errorf("команда %d должна быть обычным присваиванием: %+v", i, cmd)
		}
	}
}

// ===== Выполнение =====

func newRTPExecutor(t *testing.T) (*Executor, *Parser) {
	t.Helper()

	executor, data := newBareExecutor(t)

	factory, err := rtp.NewFactory("127.0.0.1", 22000, 23999)
	if err != nil {
		t.Fatal(err)
	}

	executor.SetRTP(factory)

	return executor, NewParser(data.Methods)
}

func runRTPScript(t *testing.T, executor *Executor, parser *Parser, text string) error {
	t.Helper()

	s, err := parser.Parse(text)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}

	return executor.Execute(s)
}

func portIsFree(port int) bool {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		return false
	}

	conn.Close()

	return true
}

func TestRTPOpenStoresHandle(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	if err := runRTPScript(t, executor, parser, "r = rtp_open(codecs = \"PCMA\", ptime = 30)\nport = r.port\nsdp = r.sdp\nid = r.id\nip = r.ip\n"); err != nil {
		t.Fatal(err)
	}

	port, _ := varValue(executor, "port")
	sdpText, _ := varValue(executor, "sdp")
	id, _ := varValue(executor, "id")
	ip, _ := varValue(executor, "ip")

	if port == "" || strings.Contains(port, "e+") {
		t.Errorf("port = %q: ожидалось обычное число", port)
	}

	if id == "" || ip != "127.0.0.1" {
		t.Errorf("id = %q, ip = %q", id, ip)
	}

	for _, want := range []string{"m=audio " + port + " RTP/AVP 8\r\n", "a=ptime:30\r\n", "a=rtpmap:8 PCMA/8000\r\n"} {
		if !strings.Contains(sdpText, want) {
			t.Errorf("в r.sdp нет %q:\n%s", want, sdpText)
		}
	}
}

func TestRTPOpenFromGivenSDP(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	script := "offer = \"v=0\\r\\nc=IN IP4 127.0.0.1\\r\\nt=0 0\\r\\nm=audio 22500 RTP/AVP 0\\r\\na=ptime:10\\r\\n\"\nr = rtp_open(offer)\nport = r.port\nsame = r.sdp\n"

	// сеанс остаётся открытым, пока идёт Execute: проверяем порт изнутри нельзя, поэтому сверяем переменные
	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	port, _ := varValue(executor, "port")
	same, _ := varValue(executor, "same")
	offer, _ := varValue(executor, "offer")

	if port != "22500" || same != offer {
		t.Errorf("port = %q, sdp совпадает с исходным: %v", port, same == offer)
	}
}

func TestRTPEndToEnd(t *testing.T) {
	executor, parser := newRTPExecutor(t)
	dir := t.TempDir()

	script := `
a = rtp_open(codecs = "PCMA")
b = rtp_open(codecs = "PCMA")
rtp_start(a, b.sdp, tone = 440)
rtp_start(b, a.sdp, tone = 1000, record = "` + filepath.ToSlash(dir) + `/b_{id}.wav")
sleep(0.5)
mid = rtp_stats(a)
sa = rtp_stop(a)
sb = rtp_stop(b)
if sa.received_packets > 10 {
    a_ok = 1
} else {
    a_ok = 0
}
if sb.received_packets > 10 {
    b_ok = 1
} else {
    b_ok = 0
}
codec = sa.codec
sent_mid = mid.sent_packets
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for name, want := range map[string]string{"a_ok": "1", "b_ok": "1", "codec": "PCMA"} {
		if got, _ := varValue(executor, name); got != want {
			t.Errorf("%s = %q, ожидалось %q", name, got, want)
		}
	}

	if got, _ := varValue(executor, "sent_mid"); got == "" || got == "0" {
		t.Errorf("rtp_stats в середине потока: sent_packets = %q", got)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "b_*.wav"))
	if len(files) != 1 {
		t.Fatalf("ожидался один файл записи, найдено %d", len(files))
	}

	if info, err := os.Stat(files[0]); err != nil || info.Size() < 2000 {
		t.Errorf("файл записи пуст или не создан: %v", err)
	}
}

func TestRTPStopsWhenScenarioEnds(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	// порты внутри цикла; сценарий сам ничего не останавливает
	script := "for i in 1..3 {\n    r = rtp_open()\n    port = r.port\n    rtp_start(r, r.sdp)\n}\n"

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	if executor.rtpSessions != nil {
		t.Error("после Execute сеансы должны быть сняты с учёта")
	}

	port, _ := varValue(executor, "port")
	if p, ok := atoi(port); !ok || !portIsFree(p) {
		t.Errorf("порт %q должен освободиться после сценария", port)
	}
}

func TestRTPStopsWhenScenarioFails(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	err := runRTPScript(t, executor, parser, "r = rtp_open()\nport = r.port\nbroken = r.nothing\n")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	port, _ := varValue(executor, "port")
	if p, ok := atoi(port); !ok || !portIsFree(p) {
		t.Errorf("порт %q должен освободиться и после ошибки", port)
	}
}

func atoi(s string) (int, bool) {
	n := 0

	if s == "" {
		return 0, false
	}

	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}

		n = n*10 + int(ch-'0')
	}

	return n, true
}

func TestRTPExecutionErrors(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"сеанс не открыт", "rtp_stop(nothing)\n", "непонятное rvalue"},
		{"чужой номер", "x = 999999\nrtp_stop(x)\n", "не найден"},
		{"tone текстом", "r = rtp_open()\nrtp_start(r, r.sdp, tone = \"abc\")\n", "tone"},
		{"tone отрицательный", "r = rtp_open()\nrtp_start(r, r.sdp, tone = -5)\n", "tone"},
		{"send не bool", "r = rtp_open()\nrtp_start(r, r.sdp, send = maybe)\n", "непонятное rvalue"},
		{"send строкой", "r = rtp_open()\nrtp_start(r, r.sdp, send = \"maybe\")\n", "send: ожидается true или false"},
		{"port не число", "r = rtp_open(port = \"abc\")\n", "port: ожидается число"},
		{"неизвестный кодек", "r = rtp_open(codecs = \"opus\")\n", "не поддерживается"},
		{"пустые кодеки", "r = rtp_open(codecs = \",\")\n", "пустой список"},
		{"ptime", "r = rtp_open(ptime = 5000)\n", "ptime"},
		{"sdp и параметры вместе", "o = \"v=0\\r\\nm=audio 22600 RTP/AVP 0\\r\\n\"\nr = rtp_open(o, ptime = 20)\n", "нельзя сочетать"},
		{"sdp без audio", "o = \"v=0\\r\\n\"\nr = rtp_open(o)\n", "нет раздела m=audio"},
		{"ответ без кодека", "r = rtp_open()\nans = \"v=0\\r\\nc=IN IP4 127.0.0.1\\r\\nm=audio 4000 RTP/AVP 97\\r\\na=rtpmap:97 opus/48000/2\\r\\n\"\nrtp_start(r, ans)\n", "нет поддерживаемого кодека"},
		{"адрес 0.0.0.0 без хоста", "r = rtp_open()\nans = \"v=0\\r\\nc=IN IP4 0.0.0.0\\r\\nm=audio 4000 RTP/AVP 0\\r\\n\"\nrtp_start(r, ans)\n", "задайте host"},
	}

	for _, c := range cases {
		executor, parser := newRTPExecutor(t)

		err := runRTPScript(t, executor, parser, c.script)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

func TestRTPNotConfigured(t *testing.T) {
	executor, data := newBareExecutor(t)

	err := executor.Execute(mustParse(t, data, "r = rtp_open()\n"))
	if err == nil || !strings.Contains(err.Error(), "RTP не настроен") {
		t.Fatalf("ожидалась ошибка «RTP не настроен», получили: %v", err)
	}
}

// Хост из ответа 0.0.0.0 заменяется адресом MM; явный host главнее всего
func TestRTPHostParam(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	script := "r = rtp_open()\nans = \"v=0\\r\\nc=IN IP4 0.0.0.0\\r\\nm=audio 4000 RTP/AVP 0\\r\\n\"\nrtp_start(r, ans, host = \"127.0.0.1\", send = false)\nst = rtp_stats(r)\nremote = st.remote\n"

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	if got, _ := varValue(executor, "remote"); got != "127.0.0.1:4000" {
		t.Errorf("remote = %q", got)
	}
}

func TestHostFromTarget(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7001":               "127.0.0.1",
		"passthrough:///10.0.0.5:7001": "10.0.0.5",
		"dns:///mm.local:7001":         "mm.local",
		"[::1]:7001":                   "::1",
		"без-порта":                    "",
		"":                             "",
		"unix:///var/run/mm.sock":      "",
	}

	for target, want := range cases {
		if got := hostFromTarget(target); got != want {
			t.Errorf("hostFromTarget(%q) = %q, ожидалось %q", target, got, want)
		}
	}
}

// maxAbsSample читает WAV, записанный тестером, и возвращает наибольшую амплитуду и число отсчётов
func maxAbsSample(t *testing.T, path string) (int, int) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) < 44 {
		t.Fatalf("%s: файл короче заголовка WAV", path)
	}

	maxAbs := 0
	count := (len(data) - 44) / 2

	for i := 0; i < count; i++ {
		v := int(int16(uint16(data[44+2*i]) | uint16(data[45+2*i])<<8))
		if v < 0 {
			v = -v
		}

		if v > maxAbs {
			maxAbs = v
		}
	}

	return maxAbs, count
}

// Параметры tone и send доходят до потока: тишина слышна как нулевая запись, send=false — как ноль отправленных пакетов
func TestRTPToneAndSendParams(t *testing.T) {
	executor, parser := newRTPExecutor(t)
	dir := filepath.ToSlash(t.TempDir())

	script := `
a = rtp_open(codecs = "PCMU")
b = rtp_open(codecs = "PCMU")
rtp_start(a, b.sdp, tone = 0, record = "` + dir + `/a.wav")
rtp_start(b, a.sdp, record = "` + dir + `/b.wav")
sleep(0.4)
sa = rtp_stop(a)
sb = rtp_stop(b)
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	// A отправляла тишину (tone = 0), поэтому B записал нули; B отправлял тон по умолчанию, и A его слышит
	if amp, n := maxAbsSample(t, dir+"/b.wav"); n < 1000 || amp != 0 {
		t.Errorf("запись B: %d отсчётов, амплитуда %d: ожидалась тишина", n, amp)
	}

	if amp, n := maxAbsSample(t, dir+"/a.wav"); n < 1000 || amp < 3000 {
		t.Errorf("запись A: %d отсчётов, амплитуда %d: ожидался тон", n, amp)
	}

	executor, parser = newRTPExecutor(t)

	script = `
a = rtp_open()
b = rtp_open()
rtp_start(a, b.sdp, send = false)
rtp_start(b, a.sdp)
sleep(0.3)
sa = rtp_stop(a)
sb = rtp_stop(b)
a_sent = sa.sent_packets
a_got = sa.received_packets
b_sent = sb.sent_packets
b_got = sb.received_packets
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	if got, _ := varValue(executor, "a_sent"); got != "0" {
		t.Errorf("send = false: A отправила %s пакетов", got)
	}

	if got, _ := varValue(executor, "b_got"); got != "0" {
		t.Errorf("B приняла %s пакетов от молчащей A", got)
	}

	for _, name := range []string{"a_got", "b_sent"} {
		if got, _ := varValue(executor, name); got == "" || got == "0" {
			t.Errorf("%s = %q: ожидалось больше нуля", name, got)
		}
	}
}

// rtp_dtmf: цифры одной стороны доходят до другой как события telephone-event
func TestRTPDTMFEndToEnd(t *testing.T) {
	executor, parser := newRTPExecutor(t)

	script := `
a = rtp_open(codecs = "PCMA,telephone-event", dtmf_pt = 96)
b = rtp_open(codecs = "PCMA,telephone-event", dtmf_pt = 96)
rtp_start(a, b.sdp)
rtp_start(b, a.sdp)
rtp_dtmf(a, "12#", duration = 40, gap = 20)
sleep(0.1)
sa = rtp_stop(a)
sb = rtp_stop(b)
sent = sa.dtmf_sent
got = sb.dtmf_received
`

	if err := runRTPScript(t, executor, parser, script); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"sent": "12#", "got": "12#"} {
		if v, _ := varValue(executor, name); v != want {
			t.Errorf("%s = %q, ожидалось %q", name, v, want)
		}
	}
}

// G.722 и L16 проходят через сценарий и выбираются по SDP-ответу
func TestRTPWideCodecs(t *testing.T) {
	for _, codec := range []string{"G722", "L16/16000"} {
		executor, parser := newRTPExecutor(t)

		script := `
a = rtp_open(codecs = "` + codec + `")
b = rtp_open(codecs = "` + codec + `")
rtp_start(a, b.sdp)
rtp_start(b, a.sdp)
sleep(0.2)
sa = rtp_stop(a)
sb = rtp_stop(b)
name = sb.codec
got = sb.received_packets
`

		if err := runRTPScript(t, executor, parser, script); err != nil {
			t.Fatalf("%s: %v", codec, err)
		}

		if got, _ := varValue(executor, "got"); got == "" || got == "0" {
			t.Errorf("%s: принято %q пакетов", codec, got)
		}
	}
}

func TestRTPDTMFErrors(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"dtmf_pt статический", "r = rtp_open(dtmf_pt = 8)\n", "dtmf_pt"},
		{"неизвестный кодек", "r = rtp_open(codecs = \"opus\")\n", "не поддерживается"},
		{"дробный тон", "r = rtp_open()\nrtp_start(r, r.sdp, tone = 440.5)\n", "tone"},
		{"до rtp_start", "r = rtp_open()\nrtp_dtmf(r, \"1\")\n", "rtp_dtmf"},
	}

	for _, c := range cases {
		executor, parser := newRTPExecutor(t)

		err := runRTPScript(t, executor, parser, c.script)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %v, ожидалась с %q", c.name, err, c.want)
		}
	}
}
