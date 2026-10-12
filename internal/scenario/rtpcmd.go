package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"tester_mm/internal/rtp"
	"tester_mm/internal/sdp"
)

// RTPOption — именованный аргумент RTP-команды: tone = 440
type RTPOption struct {
	Name  string
	Value string // выражение сценария, вычисляется при выполнении
}

// rtpSpec описывает допустимые аргументы RTP-команды
type rtpSpec struct {
	minArgs, maxArgs int
	options          []string
	usage            string
}

var rtpSpecs = map[string]rtpSpec{
	"rtp_open": {
		minArgs: 0, maxArgs: 1,
		options: []string{"ip", "port", "codecs", "ptime", "dtmf_pt"},
		usage:   "rtp = rtp_open()  или  rtp = rtp_open(sdp_offer)  или  rtp = rtp_open(codecs = \"PCMA,telephone-event\", ptime = 20)",
	},
	"rtp_start": {
		minArgs: 2, maxArgs: 2,
		options: []string{"host", "tone", "record", "send"},
		usage:   "rtp_start(rtp, chan.sdpAnswer, tone = 440, record = \"in_{id}.wav\")",
	},
	"rtp_dtmf": {
		minArgs: 2, maxArgs: 2,
		options: []string{"duration", "gap", "volume"},
		usage:   "rtp_dtmf(rtp, \"123#\", duration = 160, gap = 100, volume = 10)",
	},
	"rtp_stats": {minArgs: 1, maxArgs: 1, usage: "st = rtp_stats(rtp)"},
	"rtp_stop":  {minArgs: 1, maxArgs: 1, usage: "st = rtp_stop(rtp)"},
}

var rtpCallRe = regexp.MustCompile(`^([a-z_]+)\s*\((.*)\)$`)

var optionNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rtpKeyword возвращает имя RTP-команды, с которой начинается строка, или ""
func rtpKeyword(line string) string {
	for name := range rtpSpecs {
		// без скобки это не вызов, а переменная с похожим именем: rtp_stop = rtp_open
		if startsWithKeyword(line, name) && strings.HasPrefix(strings.TrimSpace(line[len(name):]), "(") {
			return name
		}
	}

	return ""
}

// parseRTP разбирает вызов RTP-команды; expr — вся конструкция, начиная с имени команды
func (parser *Parser) parseRTP(expr, name string, lineNum int) (Command, error) {
	spec := rtpSpecs[name]
	syntax := fmt.Errorf("строка %d: неверный синтаксис %s, ожидается: %s", lineNum, name, spec.usage)

	m := rtpCallRe.FindStringSubmatch(expr)
	if m == nil || m[1] != name {
		return Command{}, syntax
	}

	cmd := Command{IsRTP: true, RTPOp: name}

	if strings.TrimSpace(m[2]) != "" {
		for _, arg := range splitArgs(m[2]) {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				return Command{}, fmt.Errorf("строка %d: %s: пустой аргумент", lineNum, name)
			}

			optName, optValue, isOption, err := splitOption(arg)
			if err != nil {
				return Command{}, fmt.Errorf("строка %d: %s: %v", lineNum, name, err)
			}

			if !isOption {
				cmd.RTPArgs = append(cmd.RTPArgs, arg)
				continue
			}

			if !contains(spec.options, optName) {
				allowed := "именованных аргументов нет"
				if len(spec.options) > 0 {
					allowed = "допустимы: " + strings.Join(spec.options, ", ")
				}

				return Command{}, fmt.Errorf("строка %d: %s: неизвестный параметр %q (%s)", lineNum, name, optName, allowed)
			}

			for _, existing := range cmd.RTPOpts {
				if existing.Name == optName {
					return Command{}, fmt.Errorf("строка %d: %s: параметр %q указан дважды", lineNum, name, optName)
				}
			}

			cmd.RTPOpts = append(cmd.RTPOpts, RTPOption{Name: optName, Value: optValue})
		}
	}

	if len(cmd.RTPArgs) < spec.minArgs || len(cmd.RTPArgs) > spec.maxArgs {
		return Command{}, syntax
	}

	return cmd, nil
}

// splitOption делит "имя = значение". Значение без имени — позиционный аргумент.
// Знак = внутри кавычек не считается; сравнения (==, !=, <=, >=) в аргументах RTP-команд не поддерживаются.
func splitOption(arg string) (name, value string, isOption bool, err error) {
	inQuotes := false

	for i := 0; i < len(arg); i++ {
		ch := arg[i]

		if ch == '"' {
			inQuotes = !inQuotes
			continue
		}

		if inQuotes || ch != '=' {
			continue
		}

		if (i+1 < len(arg) && arg[i+1] == '=') || (i > 0 && strings.IndexByte("!<>", arg[i-1]) >= 0) {
			return "", "", false, fmt.Errorf("сравнение в аргументе %q не поддерживается", arg)
		}

		name = strings.TrimSpace(arg[:i])
		value = strings.TrimSpace(arg[i+1:])

		if !optionNameRe.MatchString(name) {
			return "", "", false, fmt.Errorf("слева от = должно быть имя параметра, получено %q", name)
		}

		if value == "" {
			return "", "", false, fmt.Errorf("у параметра %q нет значения", name)
		}

		return name, value, true, nil
	}

	return "", "", false, nil
}

func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}

	return false
}

// ===== Выполнение =====

// SetRTP задаёт фабрику RTP-сеансов; без неё rtp_open недоступен
func (executor *Executor) SetRTP(factory *rtp.Factory) {
	executor.rtpFactory = factory
}

// runRTP выполняет rtp_open / rtp_start / rtp_dtmf / rtp_stats / rtp_stop
func (executor *Executor) runRTP(label string, cmd Command) error {
	if executor.rtpFactory == nil {
		return fmt.Errorf("%s: RTP не настроен", cmd.RTPOp)
	}

	options, err := executor.rtpOptions(cmd)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.RTPOp, err)
	}

	switch cmd.RTPOp {
	case "rtp_open":
		return executor.rtpOpen(label, cmd, options)
	case "rtp_start":
		return executor.rtpStart(label, cmd, options)
	case "rtp_dtmf":
		return executor.rtpDTMF(label, cmd, options)
	default:
		return executor.rtpFinish(label, cmd)
	}
}

// rtpOptions вычисляет значения именованных аргументов
func (executor *Executor) rtpOptions(cmd Command) (map[string]string, error) {
	options := make(map[string]string, len(cmd.RTPOpts))

	for _, opt := range cmd.RTPOpts {
		value, err := executor.exprParser.ResolveValue(opt.Value)
		if err != nil {
			return nil, fmt.Errorf("параметр %s: %w", opt.Name, err)
		}

		options[opt.Name] = value
	}

	return options, nil
}

func (executor *Executor) rtpOpen(label string, cmd Command, options map[string]string) error {
	var opts rtp.OpenOptions

	if len(cmd.RTPArgs) == 1 {
		if len(options) > 0 {
			return fmt.Errorf("rtp_open: готовый SDP нельзя сочетать с параметрами (%s)", strings.Join(sortedKeys(options), ", "))
		}

		offer, err := executor.exprParser.ResolveValue(cmd.RTPArgs[0])
		if err != nil {
			return fmt.Errorf("rtp_open: SDP: %w", err)
		}

		desc, err := sdp.Parse(offer)
		if err != nil {
			return fmt.Errorf("rtp_open: %w", err)
		}

		audio, err := desc.Audio()
		if err != nil {
			return fmt.Errorf("rtp_open: %w", err)
		}

		if audio.Port == 0 {
			return fmt.Errorf("rtp_open: в SDP порт 0")
		}

		opts = rtp.OpenOptions{IP: audio.Addr, Port: audio.Port, Offer: offer, PTime: audio.PTime}
	} else {
		var err error

		if opts, err = openOptionsFrom(options); err != nil {
			return fmt.Errorf("rtp_open: %w", err)
		}
	}

	session, err := executor.rtpFactory.Open(opts)
	if err != nil {
		return fmt.Errorf("rtp_open: %w", err)
	}

	if executor.rtpSessions == nil {
		executor.rtpSessions = map[string]*rtp.Session{}
	}

	executor.rtpSessions[session.ID()] = session

	handle, _ := json.Marshal(map[string]interface{}{
		"id":   session.ID(),
		"ip":   session.IP(),
		"port": session.Port(),
		"sdp":  session.Offer(),
	})

	executor.tracef("[%s] rtp_open: сеанс %s, UDP %s:%d", label, session.ID(), session.IP(), session.Port())

	if cmd.VarName != "" {
		executor.exprParser.vars.Set(cmd.VarName, string(handle))
		executor.tracef("[%s] сеанс сохранён в переменную '%s' (поля id, ip, port, sdp)", label, cmd.VarName)
	}

	return nil
}

func openOptionsFrom(options map[string]string) (rtp.OpenOptions, error) {
	opts := rtp.OpenOptions{IP: options["ip"]}

	if v, ok := options["port"]; ok {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return opts, fmt.Errorf("port: ожидается число 1-65535, получено %q", v)
		}

		opts.Port = port
	}

	if v, ok := options["ptime"]; ok {
		ptime, err := strconv.Atoi(v)
		if err != nil {
			return opts, fmt.Errorf("ptime: ожидается число миллисекунд, получено %q", v)
		}

		opts.PTime = ptime
	}

	if v, ok := options["dtmf_pt"]; ok {
		pt, err := strconv.Atoi(v)
		if err != nil || pt < 96 || pt > 127 {
			return opts, fmt.Errorf("dtmf_pt: ожидается динамический payload type 96-127, получено %q", v)
		}

		opts.DTMFPT = pt
	}

	if v, ok := options["codecs"]; ok {
		for _, name := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			codec, err := rtp.ParseCodec(name)
			if err != nil {
				return opts, fmt.Errorf("codecs: %w (поддерживаются: %s)", err, rtp.SupportedList)
			}

			opts.Codecs = append(opts.Codecs, codec)
		}

		if len(opts.Codecs) == 0 {
			return opts, fmt.Errorf("codecs: пустой список")
		}
	}

	return opts, nil
}

func (executor *Executor) rtpStart(label string, cmd Command, options map[string]string) error {
	session, err := executor.rtpSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("rtp_start: %w", err)
	}

	remote, err := executor.exprParser.ResolveValue(cmd.RTPArgs[1])
	if err != nil {
		return fmt.Errorf("rtp_start: SDP: %w", err)
	}

	start := rtp.StartOptions{
		RemoteSDP:   remote,
		Host:        options["host"],
		DefaultHost: executor.mmHost(),
		Record:      options["record"],
		ToneHz:      -1, // по умолчанию — тон rtp.DefaultToneHz
	}

	if v, ok := options["tone"]; ok {
		hz, err := strconv.ParseFloat(v, 64)
		if err != nil || hz < 0 || hz > 4000 || hz != math.Trunc(hz) {
			return fmt.Errorf("rtp_start: tone: ожидается целая частота в Гц от 0 (тишина) до 4000, получено %q", v)
		}

		start.ToneHz = hz
	}

	if v, ok := options["send"]; ok {
		switch v {
		case "true":
		case "false":
			start.NoSend = true
		default:
			return fmt.Errorf("rtp_start: send: ожидается true или false, получено %q", v)
		}
	}

	if err := session.Start(start); err != nil {
		return fmt.Errorf("rtp_start: %w", err)
	}

	stats := session.Stats()
	executor.tracef("[%s] rtp_start: сеанс %s, %s, %s → %s%s", label, session.ID(), stats.Codec, stats.Local, stats.Remote, startNotes(start, session.RecordPath()))

	return nil
}

// rtpDTMF отправляет цифры и ждёт, пока они переданы целиком
func (executor *Executor) rtpDTMF(label string, cmd Command, options map[string]string) error {
	session, err := executor.rtpSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("rtp_dtmf: %w", err)
	}

	digits, err := executor.exprParser.ResolveValue(cmd.RTPArgs[1])
	if err != nil {
		return fmt.Errorf("rtp_dtmf: цифры: %w", err)
	}

	values := map[string]int{
		"duration": rtp.DefaultDTMFDurationMs,
		"gap":      rtp.DefaultDTMFGapMs,
		"volume":   rtp.DefaultDTMFVolume,
	}

	for name := range values {
		v, ok := options[name]
		if !ok {
			continue
		}

		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("rtp_dtmf: %s: ожидается целое число, получено %q", name, v)
		}

		values[name] = n
	}

	executor.tracef("[%s] rtp_dtmf: сеанс %s, цифры %q, по %d мс, пауза %d мс", label, session.ID(), digits, values["duration"], values["gap"])

	if err := session.SendDTMF(digits, values["duration"], values["gap"], values["volume"]); err != nil {
		return fmt.Errorf("rtp_dtmf: %w", err)
	}

	return nil
}

// startNotes описывает необычные режимы потока для строки лога; recordPath — файл записи с подставленным номером сеанса
func startNotes(start rtp.StartOptions, recordPath string) string {
	var notes []string

	if start.NoSend {
		notes = append(notes, "только приём")
	}

	if start.Record != "" {
		notes = append(notes, "запись в "+recordPath)
	}

	if len(notes) == 0 {
		return ""
	}

	return " (" + strings.Join(notes, ", ") + ")"
}

// rtpFinish выполняет rtp_stats и rtp_stop: пишет итоги в лог и, если задана переменная, сохраняет их JSON
func (executor *Executor) rtpFinish(label string, cmd Command) error {
	session, err := executor.rtpSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.RTPOp, err)
	}

	var stats rtp.Stats

	if cmd.RTPOp == "rtp_stop" {
		stats = session.Stop()
	} else {
		stats = session.Stats()
	}

	executor.tracef("[%s] %s: сеанс %s, отправлено %d, принято %d, потеряно %d (%.1f%%), джиттер %.2f мс%s",
		label, cmd.RTPOp, session.ID(), stats.SentPackets, stats.ReceivedPackets, stats.LostPackets, stats.LossPercent, stats.JitterMs, dtmfSummary(stats))

	if cmd.RTPOp == "rtp_stop" {
		executor.noteFinal(session.ID(), stats)
	}

	if cmd.VarName != "" {
		data, _ := json.Marshal(stats)
		executor.exprParser.vars.Set(cmd.VarName, string(data))
	}

	if cmd.RTPOp == "rtp_stop" {
		if err := session.RecordError(); err != nil {
			return fmt.Errorf("rtp_stop: запись звука: %w", err)
		}
	}

	return nil
}

// dtmfSummary добавляет в строку итогов цифры, если они были
func dtmfSummary(stats rtp.Stats) string {
	var parts []string

	if stats.DTMFSent != "" {
		parts = append(parts, fmt.Sprintf("DTMF отправлено %q", stats.DTMFSent))
	}

	if stats.DTMFReceived != "" {
		parts = append(parts, fmt.Sprintf("DTMF принято %q", stats.DTMFReceived))
	}

	if len(parts) == 0 {
		return ""
	}

	return ", " + strings.Join(parts, ", ")
}

// rtpSession находит сеанс по переменной-описателю (rtp) или по самому номеру
func (executor *Executor) rtpSession(expr string) (*rtp.Session, error) {
	value, err := executor.exprParser.ResolveValue(expr)
	if err != nil {
		return nil, err
	}

	id := value
	if fromHandle, err := jsonFieldPath(value, []string{"id"}); err == nil {
		id = fromHandle
	}

	session, ok := executor.rtpSessions[id]
	if !ok {
		return nil, fmt.Errorf("RTP-сеанс %q не найден: сначала выполните rtp = rtp_open()", id)
	}

	return session, nil
}

// closeRTP останавливает потоки, которые сценарий не остановил сам, чтобы порты не оставались занятыми
func (executor *Executor) closeRTP() {
	ids := make([]string, 0, len(executor.rtpSessions))
	for id := range executor.rtpSessions {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		session := executor.rtpSessions[id]

		if session.Running() {
			executor.tracef("RTP-сеанс %s не остановлен в сценарии: останавливаем (добавьте rtp_stop, чтобы получить итоги)", id)
		}

		executor.noteFinal(id, session.Stop())
	}

	executor.rtpSessions = nil
}

// mmHost возвращает адрес MM, к которому подключён тестер: туда по умолчанию шлётся RTP, если в SDP ответа 0.0.0.0
func (executor *Executor) mmHost() string {
	if executor.conn == nil {
		return ""
	}

	return hostFromTarget(executor.conn.Target())
}

// hostFromTarget достаёт адрес из цели gRPC-соединения: "10.0.0.5:7001", "dns:///mm.local:7001"
func hostFromTarget(target string) string {
	if i := strings.Index(target, ":///"); i >= 0 {
		target = target[i+4:]
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return ""
	}

	return host
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
