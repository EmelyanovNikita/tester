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
	Codecs []Codec // что предлагать в SDP; пусто — все поддерживаемые
	PTime  int     // мс; 0 — DefaultPTime
	Offer  string  // готовый SDP-offer вместо собранного (порт и адрес в нём должны совпадать с IP и Port)
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
		codecs = Supported
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
		id:     strconv.FormatUint(sessionCounter.Add(1), 10),
		conn:   conn,
		ip:     ip,
		port:   conn.LocalAddr().(*net.UDPAddr).Port,
		ptime:  ptime,
		done:   make(chan struct{}),
		sample: make([]int16, 0),
	}

	session.offer = opts.Offer
	if session.offer == "" {
		payloads := make([]sdp.PayloadCodec, len(codecs))
		for i, c := range codecs {
			payloads[i] = sdp.PayloadCodec{PayloadType: c.PayloadType, Codec: sdp.Codec{Name: c.Name, ClockRate: c.ClockRate, Channels: 1}}
		}

		session.offer = sdp.Offer{IP: ip, Port: session.port, Codecs: payloads, PTime: ptime}.String()
	}

	return session, nil
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
	RemoteSDP   string  // ответ MM: оттуда берутся адрес, порт, кодек и ptime
	Host        string  // принудительный адрес получателя вместо адреса из SDP
	DefaultHost string  // адрес, если в SDP его нет или он 0.0.0.0
	ToneHz      float64 // частота тона; 0 — тишина; отрицательное — DefaultToneHz
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

	// OtherPayloadPackets — пакеты с другим payload type (например DTMF), не учтённые выше
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
	received uint64
}

// Session — один RTP-поток: сокет, отправка и приём
type Session struct {
	id    string
	conn  *net.UDPConn
	ip    string
	port  int
	ptime int
	offer string

	done chan struct{}
	wg   sync.WaitGroup

	mu      sync.Mutex
	started bool
	stopped bool
	final   Stats

	// заполняется в Start
	codec     Codec
	remote    *net.UDPAddr
	remotePT  uint8
	startedAt time.Time
	recorder  *wavWriter
	sample    []int16

	// приём
	src          *source
	lostBefore   uint64 // потери от прежних источников
	jitter       float64
	firstArrival time.Time
	firstTS      uint32
	prevTransit  float64
	haveTransit  bool
	recNext      uint32
	recInit      bool
	recErr       error
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

	send := !opts.NoSend && audio.Direction != "sendonly" && audio.Direction != "inactive"

	toneHz := opts.ToneHz
	if toneHz < 0 {
		toneHz = DefaultToneHz
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

		s.recorder, err = createWAV(path, codec.ClockRate)
		if err != nil {
			return err
		}
	}

	s.started = true
	s.codec = codec
	s.remote = remote
	s.remotePT = uint8(payloadType)
	s.startedAt = time.Now()
	s.ptime = ptime
	s.stats = Stats{
		Codec:  codec.Name,
		Local:  s.conn.LocalAddr().String(),
		Remote: remote.String(),
	}

	s.wg.Add(1)
	go s.receiveLoop()

	if send {
		s.wg.Add(1)
		go s.sendLoop(newTone(toneHz, codec.ClockRate))
	}

	return nil
}

// chooseCodec выбирает первый кодек из ответа, который умеет тестер, и его номер payload type из SDP
func chooseCodec(audio *sdp.Media) (Codec, int, error) {
	var offered []string

	for _, pt := range audio.Payloads {
		info, ok := audio.Codecs[pt]
		if !ok {
			offered = append(offered, strconv.Itoa(pt))
			continue
		}

		offered = append(offered, info.Name)

		if codec, ok := CodecByName(info.Name); ok && info.ClockRate == codec.ClockRate {
			return codec, pt, nil
		}
	}

	return Codec{}, 0, fmt.Errorf("rtp: в ответе MM нет поддерживаемого кодека (есть: %s; поддерживаются: %s)",
		strings.Join(offered, ", "), SupportedNames())
}

func (s *Session) sendLoop(gen *tone) {
	defer s.wg.Done()

	samples := s.codec.ClockRate * s.ptime / 1000
	pcm := make([]int16, samples)
	packet := make([]byte, headerLen+samples)
	interval := time.Duration(s.ptime) * time.Millisecond

	header := Header{
		PayloadType: s.remotePT,
		Seq:         uint16(rand.Uint32()),
		Timestamp:   rand.Uint32(),
		SSRC:        rand.Uint32(),
		Marker:      true,
	}

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	base := time.Now()

	for n := 0; ; n++ {
		gen.fill(pcm)

		header.put(packet)

		for i, sample := range pcm {
			packet[headerLen+i] = s.codec.Encode(sample)
		}

		if _, err := s.conn.WriteToUDP(packet, s.remote); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// получатель может быть ещё не готов (ICMP): продолжаем, ошибки в счётчик не идут
		} else {
			s.mu.Lock()
			s.stats.SentPackets++
			s.stats.SentBytes += uint64(len(packet))
			s.mu.Unlock()
		}

		header.Marker = false
		header.Seq++
		header.Timestamp += uint32(samples)

		next := base.Add(time.Duration(n+1) * interval)
		wait := time.Until(next)

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

	if header.PayloadType != s.remotePT {
		s.stats.OtherPayloadPackets++
		return
	}

	if !s.trackSequence(header) {
		s.stats.Duplicates++
		return
	}

	s.stats.ReceivedPackets++
	s.stats.ReceivedBytes += uint64(len(data))

	s.updateJitter(header, arrival)
	s.record(header, payload)
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

// record дописывает звук пакета в WAV; на месте потерянных пакетов вставляется тишина
func (s *Session) record(header Header, payload []byte) {
	if s.recorder == nil || s.recErr != nil {
		return
	}

	if s.recInit {
		gap := int32(header.Timestamp - s.recNext)

		switch {
		case gap < 0:
			return // запоздавший пакет: его место уже заполнено тишиной
		case gap > 0 && int(gap) <= maxSilenceFill*s.codec.ClockRate:
			s.sample = append(s.sample[:0], make([]int16, gap)...)
			s.recErr = s.recorder.write(s.sample)
		}
	}

	s.sample = s.sample[:0]
	for _, b := range payload {
		s.sample = append(s.sample, s.codec.Decode(b))
	}

	if s.recErr == nil {
		s.recErr = s.recorder.write(s.sample)
	}

	s.recNext = header.Timestamp + uint32(len(payload))
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

// snapshot вызывается под s.mu
func (s *Session) snapshot() Stats {
	st := s.stats

	if s.started {
		st.DurationSec = time.Since(s.startedAt).Seconds()
		st.JitterMs = s.jitter / float64(s.codec.ClockRate) * 1000
	}

	st.LostPackets = s.lostBefore
	if s.src != nil {
		st.LostPackets += lost(s.src)
	}

	if total := st.ReceivedPackets + st.LostPackets; total > 0 {
		st.LossPercent = float64(st.LostPackets) / float64(total) * 100
	}

	return st
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
		s.final.DurationSec = time.Since(s.startedAt).Seconds()
	}

	return s.final
}

// Running сообщает, что поток запущен и ещё не остановлен
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started && !s.stopped
}

// RecordError — ошибка записи WAV, если она была (проверяется после Stop)
func (s *Session) RecordError() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.recErr
}
