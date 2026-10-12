package rtp

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tester_mm/internal/sdp"
)

const (
	// DefaultPTime — длительность пакета в мс, если ни предложение, ни ответ её не задали
	DefaultPTime = 20

	// DefaultToneHz — частота тона в потоке по умолчанию
	DefaultToneHz = 440

	// maxSilenceFill — сколько тишины вставлять в запись на месте потерянных пакетов (в секундах)
	maxSilenceFill = 1

	// maxDropout — скачок номера пакета вперёд, после которого источник считается новым (RFC 3550, A.1)
	maxDropout = 3000

	// maxMisorder — на сколько пакет может отстать, чтобы считаться перепутанным, а не новым источником
	maxMisorder = 100

	// behindLimit — если отправка отстала от расписания больше чем на это время, расписание начинается заново
	behindLimit = time.Second
)

// Factory выдаёт сокеты: свободные порты берутся из заданного диапазона. Безопасна для параллельного использования.
type Factory struct {
	ip      string
	portMin int
	portMax int

	mu   sync.Mutex
	next int
}

// NewFactory проверяет настройки и создаёт фабрику. Порты выдаются чётные, как принято для RTP.
func NewFactory(ip string, portMin, portMax int) (*Factory, error) {
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("rtp: неверный ip %q", ip)
	}

	if portMin < 1 || portMax > 65535 || portMin > portMax {
		return nil, fmt.Errorf("rtp: неверный диапазон портов %d-%d (ожидается 1-65535, min <= max)", portMin, portMax)
	}

	start := portMin
	if start%2 != 0 {
		start++
	}

	return &Factory{ip: ip, portMin: portMin, portMax: portMax, next: start}, nil
}

// IP — адрес, который используется, если он не задан при открытии
func (f *Factory) IP() string { return f.ip }

// OpenOptions — параметры нового сеанса
type OpenOptions struct {
	IP     string  // локальный адрес; пусто — адрес фабрики
	Port   int     // 0 — взять свободный из диапазона
	Codecs []Codec // что предлагать в SDP, по убыванию предпочтения; пусто — DefaultOffer
	DTMFPT int     // payload type telephone-event в SDP; 0 — DefaultDTMFPT
	PTime  int     // мс; 0 — DefaultPTime
	Offer  string  // готовый SDP вместо собранного (порт и адрес в нём должны совпадать с IP и Port)
}

var sessionCounter atomic.Uint64

// Open занимает UDP-порт и возвращает сеанс. Поток не идёт, пока не вызван Start.
func (f *Factory) Open(opts OpenOptions) (*Session, error) {
	ip := opts.IP
	if ip == "" {
		ip = f.ip
	}

	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("rtp: неверный ip %q", ip)
	}

	ptime := opts.PTime
	if ptime == 0 {
		ptime = DefaultPTime
	}

	if ptime < 1 || ptime > 200 {
		return nil, fmt.Errorf("rtp: ptime %d вне диапазона 1-200 мс", ptime)
	}

	codecs := opts.Codecs
	if len(codecs) == 0 {
		codecs = DefaultOffer
	}

	var payloads []sdp.PayloadCodec

	if opts.Offer == "" {
		var err error

		if payloads, err = assignPayloadTypes(codecs, opts.DTMFPT); err != nil {
			return nil, fmt.Errorf("rtp: %w", err)
		}
	}

	var (
		conn *net.UDPConn
		err  error
	)

	if opts.Port != 0 {
		conn, err = listenUDP(ip, opts.Port)
		if err != nil {
			return nil, fmt.Errorf("rtp: порт %d занят или недоступен: %w", opts.Port, err)
		}
	} else {
		conn, err = f.listenFree(ip)
		if err != nil {
			return nil, err
		}
	}

	session := &Session{
		id:       strconv.FormatUint(sessionCounter.Add(1), 10),
		conn:     conn,
		ip:       ip,
		port:     conn.LocalAddr().(*net.UDPAddr).Port,
		ptime:    ptime,
		done:     make(chan struct{}),
		dtmfJobs: make(chan *dtmfJob),
		dtmfPT:   -1,
	}

	session.offer = opts.Offer
	if session.offer == "" {
		session.offer = sdp.Offer{IP: ip, Port: session.port, Codecs: payloads, PTime: ptime}.String()
	}

	return session, nil
}

// assignPayloadTypes раздаёт payload type: статические — как положено кодеку, telephone-event — заданный, остальные — с 96
func assignPayloadTypes(codecs []Codec, dtmfPT int) ([]sdp.PayloadCodec, error) {
	if dtmfPT == 0 {
		dtmfPT = DefaultDTMFPT
	}

	if dtmfPT < 1 || dtmfPT > 127 {
		return nil, fmt.Errorf("payload type telephone-event %d вне диапазона 1-127", dtmfPT)
	}

	assigned := make([]int, len(codecs))
	used := map[int]string{}
	seen := map[Codec]bool{}

	for i, c := range codecs {
		if seen[c] {
			return nil, fmt.Errorf("кодек %s указан дважды", c)
		}

		seen[c] = true

		pt := c.StaticPT
		if c.kind == kindEvent {
			pt = dtmfPT
		}

		assigned[i] = pt

		if pt < 0 {
			continue
		}

		if other, taken := used[pt]; taken {
			return nil, fmt.Errorf("payload type %d нужен и для %s, и для %s", pt, other, c)
		}

		used[pt] = c.String()
	}

	next := 96

	for i, c := range codecs {
		if assigned[i] >= 0 {
			continue
		}

		for used[next] != "" {
			next++
		}

		if next > 127 {
			return nil, fmt.Errorf("не хватило динамических payload type для %s", c)
		}

		assigned[i] = next
		used[next] = c.String()
	}

	payloads := make([]sdp.PayloadCodec, len(codecs))

	for i, c := range codecs {
		payloads[i] = sdp.PayloadCodec{
			PayloadType: assigned[i],
			Codec:       sdp.Codec{Name: c.Name, ClockRate: c.ClockRate, Channels: c.Channels},
		}

		if c.kind == kindEvent {
			payloads[i].Fmtp = "0-15"
		}
	}

	return payloads, nil
}

func listenUDP(ip string, port int) (*net.UDPConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip), Port: port})
}

// listenFree перебирает чётные порты диапазона по кругу, пока не найдёт свободный
func (f *Factory) listenFree(ip string) (*net.UDPConn, error) {
	count := (f.portMax-f.portMin)/2 + 1

	for attempt := 0; attempt < count; attempt++ {
		f.mu.Lock()
		port := f.next

		f.next += 2
		if f.next > f.portMax {
			f.next = f.portMin
			if f.next%2 != 0 {
				f.next++
			}
		}
		f.mu.Unlock()

		if port > f.portMax {
			continue
		}

		if conn, err := listenUDP(ip, port); err == nil {
			return conn, nil
		}
	}

	return nil, fmt.Errorf("rtp: нет свободных портов в диапазоне %d-%d (адрес %s)", f.portMin, f.portMax, ip)
}

// StartOptions — параметры запуска потока
type StartOptions struct {
	RemoteSDP   string  // ответ MM: оттуда берутся адрес, порт, кодек, payload type и ptime
	Host        string  // принудительный адрес получателя вместо адреса из SDP
	DefaultHost string  // адрес, если в SDP его нет или он 0.0.0.0
	ToneHz      float64 // частота тона (целое число Гц); 0 — тишина; отрицательное — DefaultToneHz
	Record      string  // файл WAV для записи принятого звука; {id} заменяется номером сеанса
	NoSend      bool    // только принимать
}

// Stats — счётчики сеанса
type Stats struct {
	Codec       string  `json:"codec"`
	Local       string  `json:"local"`
	Remote      string  `json:"remote"`
	DurationSec float64 `json:"durationSec"`

	SentPackets uint64 `json:"sentPackets"`
	SentBytes   uint64 `json:"sentBytes"`

	ReceivedPackets uint64  `json:"receivedPackets"`
	ReceivedBytes   uint64  `json:"receivedBytes"`
	LostPackets     uint64  `json:"lostPackets"`
	LossPercent     float64 `json:"lossPercent"`
	OutOfOrder      uint64  `json:"outOfOrderPackets"`
	Duplicates      uint64  `json:"duplicatePackets"`
	JitterMs        float64 `json:"jitterMs"`

	// DTMFSent — цифры, отправленные rtp_dtmf; DTMFReceived — принятые события; DTMFPackets — принятые пакеты событий
	DTMFSent     string `json:"dtmfSent"`
	DTMFReceived string `json:"dtmfReceived"`
	DTMFPackets  uint64 `json:"dtmfPackets"`

	// OtherPayloadPackets — пакеты с неизвестным payload type; в потерях и счётчиках выше не участвуют
	OtherPayloadPackets uint64 `json:"otherPayloadPackets"`
	// BadPackets — датаграммы, не являющиеся RTP
	BadPackets uint64 `json:"badPackets"`
}

// source — состояние приёма от одного SSRC
type source struct {
	ssrc     uint32
	base     uint32 // первый номер пакета
	maxSeq   uint16
	cycles   uint32 // старшие 16 бит расширенного номера
	received uint64 // все принятые пакеты источника, и звук, и события
}

// Session — один RTP-поток: сокет, отправка и приём
type Session struct {
	id    string
	conn  *net.UDPConn
	ip    string
	port  int
	ptime int
	offer string

	done     chan struct{}
	dtmfJobs chan *dtmfJob
	wg       sync.WaitGroup

	sentPackets atomic.Uint64
	sentBytes   atomic.Uint64

	mu      sync.Mutex
	started bool
	stopped bool
	sending bool
	final   Stats

	// заполняется в Start
	codec      Codec
	audioPT    uint8
	dtmfPT     int // -1, если в ответе нет telephone-event
	remote     *net.UDPAddr
	startedAt  time.Time
	recorder   *wavWriter
	recordPath string
	decoder    Decoder
	pcm        []int16

	// приём
	src          *source
	lostBefore   uint64
	jitter       float64
	firstArrival time.Time
	firstTS      uint32
	prevTransit  float64
	haveTransit  bool
	recNext      uint32
	recInit      bool
	recErr       error
	lastEventTS  uint32
	haveEvent    bool
	dtmfSent     strings.Builder
	stats        Stats
}

// ID — номер сеанса, уникальный в процессе
func (s *Session) ID() string { return s.id }

// IP — локальный адрес
func (s *Session) IP() string { return s.ip }

// Port — локальный порт
func (s *Session) Port() int { return s.port }

// Offer — SDP, который нужно отправить MM
func (s *Session) Offer() string { return s.offer }

// RecordPath — файл, в который идёт запись (с подставленным номером сеанса); пусто, если записи нет
func (s *Session) RecordPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.recordPath
}

// ErrHostRequired — в ответе нет пригодного адреса и его нужно задать явно
var ErrHostRequired = errors.New("в SDP нет адреса получателя (пусто или 0.0.0.0): задайте host")

// Start разбирает ответ MM и запускает отправку и приём
func (s *Session) Start(opts StartOptions) error {
	desc, err := sdp.Parse(opts.RemoteSDP)
	if err != nil {
		return err
	}

	audio, err := desc.Audio()
	if err != nil {
		return err
	}

	if audio.Port == 0 {
		return errors.New("rtp: MM отклонил аудио-поток (порт 0 в SDP)")
	}

	codec, payloadType, err := chooseCodec(audio)
	if err != nil {
		return err
	}

	dtmfPT := chooseEventPT(audio, codec)

	host := opts.Host
	if host == "" {
		host = audio.Addr
	}

	if host == "" || host == "0.0.0.0" {
		host = opts.DefaultHost
	}

	if host == "" || host == "0.0.0.0" {
		return ErrHostRequired
	}

	remote, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(audio.Port)))
	if err != nil {
		return fmt.Errorf("rtp: адрес получателя %s:%d: %w", host, audio.Port, err)
	}

	ptime := s.ptime
	if audio.PTime > 0 {
		ptime = audio.PTime
	}

	if _, err := codec.PayloadBytes(ptime); err != nil {
		return fmt.Errorf("rtp: %w", err)
	}

	send := !opts.NoSend && audio.Direction != "sendonly" && audio.Direction != "inactive"

	toneHz := DefaultToneHz

	if opts.ToneHz >= 0 {
		if opts.ToneHz != math.Trunc(opts.ToneHz) {
			return fmt.Errorf("rtp: частота тона должна быть целым числом Гц, получено %v", opts.ToneHz)
		}

		toneHz = int(opts.ToneHz)
	}

	var loop *frameLoop

	if send {
		if loop, err = toneLoop(codec, ptime, toneHz); err != nil {
			return fmt.Errorf("rtp: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return errors.New("rtp: сеанс уже остановлен")
	}

	if s.started {
		return errors.New("rtp: сеанс уже запущен")
	}

	if opts.Record != "" {
		path := strings.ReplaceAll(opts.Record, "{id}", s.id)

		s.recorder, err = createWAV(path, codec.SampleRate, codec.Channels)
		if err != nil {
			return err
		}

		s.recordPath = path
		s.decoder = codec.NewDecoder()
	}

	s.started = true
	s.sending = send
	s.codec = codec
	s.audioPT = uint8(payloadType)
	s.dtmfPT = dtmfPT
	s.remote = remote
	s.startedAt = time.Now()
	s.ptime = ptime
	s.stats = Stats{
		Codec:  codec.String(),
		Local:  s.conn.LocalAddr().String(),
		Remote: remote.String(),
	}

	s.wg.Add(1)
	go s.receiveLoop()

	if send {
		s.wg.Add(1)
		go s.sendLoop(loop)
	}

	return nil
}

// chooseCodec выбирает первый звуковой кодек из ответа, который умеет тестер, и его номер payload type из SDP
func chooseCodec(audio *sdp.Media) (Codec, int, error) {
	var offered []string

	for _, pt := range audio.Payloads {
		info, ok := audio.Codecs[pt]
		if !ok {
			offered = append(offered, strconv.Itoa(pt))
			continue
		}

		offered = append(offered, fmt.Sprintf("%s/%d", info.Name, info.ClockRate))

		if codec, ok := MatchSDP(info.Name, info.ClockRate, info.Channels); ok && codec.IsAudio() {
			return codec, pt, nil
		}
	}

	return Codec{}, 0, fmt.Errorf("rtp: в ответе MM нет поддерживаемого кодека (есть: %s; поддерживаются: %s)",
		strings.Join(offered, ", "), SupportedList)
}

// chooseEventPT ищет telephone-event в ответе: предпочитает ту же частоту часов, что у звука; -1, если его нет
func chooseEventPT(audio *sdp.Media, codec Codec) int {
	found := -1

	for _, pt := range audio.Payloads {
		info, ok := audio.Codecs[pt]
		if !ok || !strings.EqualFold(info.Name, TelephoneEvent.Name) {
			continue
		}

		if info.ClockRate == codec.ClockRate {
			return pt
		}

		if found < 0 {
			found = pt
		}
	}

	return found
}

// SendDTMF отправляет цифры в том же потоке и возвращается, когда все они переданы.
// duration — длительность каждой цифры, gap — пауза между цифрами (в мс), volume — громкость 0..63.
func (s *Session) SendDTMF(digits string, durationMs, gapMs, volume int) error {
	events, err := ParseDTMF(digits)
	if err != nil {
		return err
	}

	if durationMs < 1 || durationMs > maxDTMFDurationMs {
		return fmt.Errorf("длительность цифры %d мс вне диапазона 1-%d", durationMs, maxDTMFDurationMs)
	}

	if gapMs < 0 || gapMs > maxDTMFDurationMs {
		return fmt.Errorf("пауза %d мс вне диапазона 0-%d", gapMs, maxDTMFDurationMs)
	}

	if volume < 0 || volume > 63 {
		return fmt.Errorf("громкость %d вне диапазона 0-63", volume)
	}

	s.mu.Lock()
	started, stopped, sending, dtmfPT, ptime := s.started, s.stopped, s.sending, s.dtmfPT, s.ptime
	s.mu.Unlock()

	switch {
	case stopped:
		return errors.New("сеанс остановлен")
	case !started:
		return errors.New("сеанс ещё не запущен: сначала rtp_start")
	case !sending:
		return errors.New("поток не отправляет RTP (send = false или направление в SDP)")
	case dtmfPT < 0:
		return errors.New("в ответе MM нет telephone-event: добавьте его в codecs при rtp_open")
	}

	packets := (durationMs + ptime - 1) / ptime

	for i, event := range events {
		job := &dtmfJob{event: event, volume: byte(volume), packets: packets, done: make(chan struct{})}

		select {
		case s.dtmfJobs <- job:
		case <-s.done:
			return errors.New("сеанс остановлен")
		}

		select {
		case <-job.done:
		case <-s.done:
			return errors.New("сеанс остановлен")
		}

		s.mu.Lock()
		s.dtmfSent.WriteByte(dtmfDigits[event])
		s.mu.Unlock()

		if gapMs > 0 && i < len(events)-1 {
			select {
			case <-time.After(time.Duration(gapMs) * time.Millisecond):
			case <-s.done:
				return errors.New("сеанс остановлен")
			}
		}
	}

	return nil
}

func (s *Session) sendLoop(loop *frameLoop) {
	defer s.wg.Done()

	clockPerFrame := uint32(s.codec.ClockPerFrame(s.ptime))
	payloadLen := len(loop.frames[0])
	packet := make([]byte, headerLen+max(payloadLen, 4))
	interval := time.Duration(s.ptime) * time.Millisecond

	header := Header{
		PayloadType: s.audioPT,
		Seq:         uint16(rand.Uint32()),
		SSRC:        rand.Uint32(),
		Marker:      true,
	}

	audioTS := rand.Uint32()
	frame := 0

	var event *dtmfState

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	base := time.Now()

	for n := 0; ; n++ {
		if event == nil {
			select {
			case job := <-s.dtmfJobs:
				event = newDTMFState(job, audioTS, clockPerFrame)
			default:
			}
		}

		var size int

		if event != nil {
			header.PayloadType = uint8(s.dtmfPT)
			header.Timestamp = event.startTS

			marker, finished := event.fill(packet[headerLen:])
			header.Marker = marker
			size = 4

			if finished {
				close(event.job.done)
				event = nil
			}
		} else {
			header.PayloadType = s.audioPT
			header.Timestamp = audioTS
			size = copy(packet[headerLen:], loop.frames[frame])

			if frame++; frame == len(loop.frames) {
				frame = 0
			}
		}

		header.put(packet)

		if _, err := s.conn.WriteToUDP(packet[:headerLen+size], s.remote); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// получатель может быть ещё не готов (ICMP): продолжаем, ошибки в счётчик не идут
		} else {
			s.sentPackets.Add(1)
			s.sentBytes.Add(uint64(headerLen + size))
		}

		header.Marker = false
		header.Seq++
		audioTS += clockPerFrame

		wait := time.Until(base.Add(time.Duration(n+1) * interval))

		if wait < -behindLimit {
			// сильно отстали (например, остановили процесс): не отправляем накопившееся пачкой
			base = time.Now()
			n = -1
			wait = interval
		}

		if wait > 0 {
			timer.Reset(wait)

			select {
			case <-s.done:
				return
			case <-timer.C:
			}
		} else {
			select {
			case <-s.done:
				return
			default:
			}
		}
	}
}

func (s *Session) receiveLoop() {
	defer s.wg.Done()

	buf := make([]byte, 2048)

	for {
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			select {
			case <-s.done:
				return
			case <-time.After(time.Millisecond):
			}

			continue
		}

		s.handlePacket(buf[:n], time.Now())
	}
}

// handlePacket учитывает входящую датаграмму в статистике и записывает звук
func (s *Session) handlePacket(data []byte, arrival time.Time) {
	header, payload, err := ParsePacket(data)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.stats.BadPackets++
		return
	}

	isAudio := header.PayloadType == s.audioPT
	isEvent := s.dtmfPT >= 0 && int(header.PayloadType) == s.dtmfPT

	if !isAudio && !isEvent {
		s.stats.OtherPayloadPackets++
		return
	}

	// звук и события идут одним потоком с общей нумерацией, поэтому учитываются вместе
	if !s.trackSequence(header) {
		s.stats.Duplicates++
		return
	}

	if isEvent {
		s.stats.DTMFPackets++
		s.onEvent(header, payload)

		return
	}

	s.stats.ReceivedPackets++
	s.stats.ReceivedBytes += uint64(len(data))

	s.updateJitter(header, arrival)
	s.record(header, payload)
}

// onEvent запоминает цифру при первом пакете события; повторы и пакеты окончания того же события (тот же timestamp) не считаются
func (s *Session) onEvent(header Header, payload []byte) {
	event, _, _, ok := parseEventPayload(payload)
	if !ok {
		s.stats.BadPackets++
		return
	}

	if s.haveEvent && header.Timestamp == s.lastEventTS {
		return
	}

	s.haveEvent = true
	s.lastEventTS = header.Timestamp

	if digit, ok := dtmfDigit(event); ok {
		s.stats.DTMFReceived += string(digit)
	}
}

// trackSequence ведёт учёт номеров пакетов по RFC 3550 (приложение A.1).
// Возвращает false для дубликата.
func (s *Session) trackSequence(header Header) bool {
	src := s.src

	if src == nil || src.ssrc != header.SSRC {
		s.startSource(header)
		return true
	}

	delta := header.Seq - src.maxSeq

	switch {
	case delta == 0:
		return false
	case delta < maxDropout:
		if header.Seq < src.maxSeq {
			src.cycles += 1 << 16
		}

		src.maxSeq = header.Seq
	case delta <= math.MaxUint16-maxMisorder+1:
		// слишком большой скачок: считаем, что источник перезапустился
		s.startSource(header)
		return true
	default:
		// пакет пришёл позже следующих
		s.stats.OutOfOrder++
	}

	src.received++

	return true
}

// startSource начинает учёт нового источника; потери прежнего сохраняются
func (s *Session) startSource(header Header) {
	if s.src != nil {
		s.lostBefore += lost(s.src)
	}

	s.src = &source{ssrc: header.SSRC, base: uint32(header.Seq), maxSeq: header.Seq, received: 1}
	s.haveTransit = false
	s.recInit = false
}

func lost(src *source) uint64 {
	expected := uint64(src.cycles+uint32(src.maxSeq)) - uint64(src.base) + 1
	if expected <= src.received {
		return 0
	}

	return expected - src.received
}

// updateJitter считает межпакетную неравномерность по RFC 3550 (6.4.1)
func (s *Session) updateJitter(header Header, arrival time.Time) {
	if !s.haveTransit {
		s.firstArrival = arrival
		s.firstTS = header.Timestamp
		s.prevTransit = 0
		s.haveTransit = true

		return
	}

	rate := float64(s.codec.ClockRate)
	arrivalUnits := arrival.Sub(s.firstArrival).Seconds() * rate
	sentUnits := float64(int32(header.Timestamp - s.firstTS))
	transit := arrivalUnits - sentUnits

	diff := math.Abs(transit - s.prevTransit)
	s.prevTransit = transit
	s.jitter += (diff - s.jitter) / 16
}

// record дописывает звук пакета в WAV; на месте потерянных пакетов (и пакетов с цифрами) вставляется тишина
func (s *Session) record(header Header, payload []byte) {
	if s.recorder == nil || s.recErr != nil {
		return
	}

	s.pcm = s.decoder.Decode(payload, s.pcm)
	frameClock := uint32(len(s.pcm) / s.codec.Channels * s.codec.ClockRate / s.codec.SampleRate)

	if s.recInit {
		gap := int32(header.Timestamp - s.recNext)

		switch {
		case gap < 0:
			return // запоздавший пакет: его место уже заполнено тишиной
		case gap > 0 && int(gap) <= maxSilenceFill*s.codec.ClockRate:
			silence := make([]int16, int(gap)*s.codec.SampleRate/s.codec.ClockRate*s.codec.Channels)
			s.recErr = s.recorder.write(silence)
		}
	}

	if s.recErr == nil {
		s.recErr = s.recorder.write(s.pcm)
	}

	s.recNext = header.Timestamp + frameClock
	s.recInit = true
}

// Stats возвращает текущие счётчики; после Stop — итоговые
func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return s.final
	}

	return s.snapshot()
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// snapshot вызывается под s.mu
func (s *Session) snapshot() Stats {
	st := s.stats

	st.SentPackets = s.sentPackets.Load()
	st.SentBytes = s.sentBytes.Load()
	st.DTMFSent = s.dtmfSent.String()

	if s.started {
		st.DurationSec = round3(time.Since(s.startedAt).Seconds())
		st.JitterMs = round3(s.jitter / float64(s.codec.ClockRate) * 1000)
	}

	st.LostPackets = s.lostBefore
	if s.src != nil {
		st.LostPackets += lost(s.src)
	}

	if total := st.ReceivedPackets + st.DTMFPackets + st.LostPackets; total > 0 {
		st.LossPercent = round3(float64(st.LostPackets) / float64(total) * 100)
	}

	return st
}

// Running сообщает, что поток запущен и ещё не остановлен
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started && !s.stopped
}

// Stop останавливает поток, освобождает порт, закрывает запись и возвращает итоговые счётчики.
// Повторный вызов безопасен и возвращает те же значения.
func (s *Session) Stop() Stats {
	s.mu.Lock()

	if s.stopped {
		defer s.mu.Unlock()
		return s.final
	}

	s.stopped = true
	s.mu.Unlock()

	close(s.done)
	s.conn.Close()
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recorder != nil {
		if err := s.recorder.close(); err != nil && s.recErr == nil {
			s.recErr = err
		}
	}

	s.final = s.snapshot()
	if s.started {
		s.final.DurationSec = round3(time.Since(s.startedAt).Seconds())
	}

	return s.final
}

// RecordError — ошибка записи WAV, если она была (проверяется после Stop)
func (s *Session) RecordError() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.recErr
}
