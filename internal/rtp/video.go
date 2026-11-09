package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"tester_mm/internal/sdp"
)

// Видео-сеанс: тестер принимает видео от MM/MSR, считает кадры и опорные кадры, принимает RTCP и умеет
// запросить опорный кадр через RTCP-FB PLI (RFC 4585). Может слать H.264 из файла (h264file.go) и на PLI
// от MSR отвечает опорным кадром.

// DefaultVideoPT — payload type видео в SDP тестера, если не задан и не взят из offer MM
const DefaultVideoPT = 96

// VideoCodec — видеокодек, который тестер умеет предлагать и разбирать
type VideoCodec struct {
	Name      string
	ClockRate int
	Fmtp      string // что пишется в a=fmtp нашего offer
}

var videoCodecs = []VideoCodec{
	{Name: "H264", ClockRate: 90000, Fmtp: "profile-level-id=42e01f;packetization-mode=1"},
	{Name: "VP8", ClockRate: 90000},
}

// VideoSupportedList — список видеокодеков для сообщений об ошибках
var VideoSupportedList = func() string {
	names := make([]string, len(videoCodecs))
	for i, c := range videoCodecs {
		names[i] = c.Name
	}

	return strings.Join(names, ", ")
}()

// ParseVideoCodec находит видеокодек по имени без учёта регистра: "H264", "vp8"
func ParseVideoCodec(name string) (VideoCodec, error) {
	for _, c := range videoCodecs {
		if strings.EqualFold(c.Name, strings.TrimSpace(name)) {
			return c, nil
		}
	}

	return VideoCodec{}, fmt.Errorf("неизвестный видеокодек %q", name)
}

// VideoOpenOptions — параметры нового видео-сеанса
type VideoOpenOptions struct {
	IP    string     // локальный адрес; пусто — адрес фабрики
	Codec VideoCodec // пусто — H264
	PT    int        // payload type; 0 — из Offer, иначе DefaultVideoPT
	// Base — SDP, к которому добавляется раздел m=video (обычно SDP аудио-сеанса того же плеча); пусто — собрать свой
	Base string
	// Offer — SDP-предложение MM, на которое отвечает тестер: из его m=video берутся payload type и fmtp кодека
	Offer string
}

// VideoStats — счётчики видео-сеанса
type VideoStats struct {
	Codec       string  `json:"codec"`
	Local       string  `json:"local"`
	Remote      string  `json:"remote"`
	RemoteSSRC  string  `json:"remote_ssrc"`
	DurationSec float64 `json:"duration_sec"`

	ReceivedPackets     uint64 `json:"received_packets"`
	ReceivedBytes       uint64 `json:"received_bytes"`
	LostPackets         uint64 `json:"lost_packets"`
	OtherPayloadPackets uint64 `json:"other_payload_packets"`

	// Frames — принятые кадры (разные RTP timestamp); Keyframes — из них опорные (IDR у H.264, key frame у VP8)
	Frames    uint64 `json:"frames"`
	Keyframes uint64 `json:"keyframes"`

	// RTCPReceived — принятые RTCP-пакеты; PLIReceived — сколько в них было PLI (MM/MSR просит опорный кадр у тестера)
	RTCPReceived uint64 `json:"rtcp_received"`
	PLIReceived  uint64 `json:"pli_received"`

	// PLISent — отправленные тестером PLI; PLIAnswered — сколько раз после PLI пришёл опорный кадр
	PLISent     uint64 `json:"pli_sent"`
	PLIAnswered uint64 `json:"pli_answered"`
	// LastPLIToKeyframeMs — время от первого неотвеченного PLI до опорного кадра в последнем ответе; -1 — ответов не было
	LastPLIToKeyframeMs float64 `json:"last_pli_to_keyframe_ms"`
	// SinceLastKeyframeMs — сколько прошло с последнего опорного кадра; -1 — опорных кадров не было
	SinceLastKeyframeMs float64 `json:"since_last_keyframe_ms"`

	// Отправка из файла: пакеты, кадры, опорные кадры; KeyframesOnPLI — опорные кадры, отправленные в ответ на PLI;
	// DroppedKeyframes — опорные кадры файла, пропущенные из-за drop_keyframes
	SentPackets      uint64 `json:"sent_packets"`
	SentFrames       uint64 `json:"sent_frames"`
	SentKeyframes    uint64 `json:"sent_keyframes"`
	KeyframesOnPLI   uint64 `json:"keyframes_on_pli"`
	DroppedKeyframes uint64 `json:"dropped_keyframes"`
}

// DefaultVideoFPS — частота кадров при отправке из файла
const DefaultVideoFPS = 25

// VideoStartOptions — параметры запуска видео-потока
type VideoStartOptions struct {
	RemoteSDP   string // SDP MM (ответ или предложение): адрес, порт, кодек
	Host        string // принудительный адрес MM
	DefaultHost string // адрес, если в SDP его нет или он 0.0.0.0
	// File — H.264 Annex B, который отправляется по кругу; пусто — только приём
	File string
	// FPS — кадров в секунду при отправке; 0 — DefaultVideoFPS
	FPS int
	// DropKeyframes — не отправлять опорные кадры файла, кроме ответов на PLI (чтобы MSR сам запросил опорный кадр)
	DropKeyframes bool
}

// VideoSession — видео-поток одного плеча: RTP-порт (чётный) и RTCP-порт (следующий)
type VideoSession struct {
	id       string
	ip       string
	port     int
	rtpConn  *net.UDPConn
	rtcpConn *net.UDPConn
	offer    string
	codec    VideoCodec
	pt       int
	ssrc     uint32

	mu         sync.Mutex
	started    bool
	stopped    bool
	startedAt  time.Time
	remoteRTCP *net.UDPAddr
	acceptPT   map[uint8]bool
	keyRequest bool // пришёл PLI: отправитель должен выдать опорный кадр

	// приём
	haveSSRC    bool
	remoteSSRC  uint32
	haveSeq     bool
	baseSeq     uint32 // расширенный номер первого пакета
	maxSeq      uint32 // расширенный наибольший номер
	haveTS      bool
	lastTS      uint32
	haveKeyTS   bool
	lastKeyTS   uint32
	lastKeyAt   time.Time
	pendingPLI  time.Time // время первого неотвеченного PLI; нулевое — неотвеченных нет
	lastPLIToKF time.Duration
	stats       VideoStats

	done  chan struct{}
	wg    sync.WaitGroup
	final VideoStats
}

// OpenVideo занимает пару UDP-портов (RTP и RTP+1 для RTCP) и собирает SDP с разделом m=video.
// Поток не принимается, пока не вызван Start.
func (f *Factory) OpenVideo(opts VideoOpenOptions) (*VideoSession, error) {
	ip := opts.IP
	if ip == "" {
		ip = f.ip
	}

	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("rtp: неверный ip %q", ip)
	}

	codec := opts.Codec
	if codec.Name == "" {
		codec = videoCodecs[0]
	}

	pt := opts.PT
	fmtp := codec.Fmtp

	if opts.Offer != "" {
		offerPT, offerFmtp, err := videoPTFromOffer(opts.Offer, codec)
		if err != nil {
			return nil, err
		}

		if pt == 0 {
			pt = offerPT
		}

		if offerFmtp != "" {
			fmtp = offerFmtp
		}
	}

	if pt == 0 {
		pt = DefaultVideoPT
	}

	if pt < 96 || pt > 127 {
		return nil, fmt.Errorf("rtp: payload type видео %d вне диапазона 96-127", pt)
	}

	rtpConn, rtcpConn, err := f.listenPair(ip)
	if err != nil {
		return nil, err
	}

	session := &VideoSession{
		id:       "v" + strconv.FormatUint(sessionCounter.Add(1), 10),
		ip:       ip,
		port:     rtpConn.LocalAddr().(*net.UDPAddr).Port,
		rtpConn:  rtpConn,
		rtcpConn: rtcpConn,
		codec:    codec,
		pt:       pt,
		ssrc:     rand.Uint32(),
		done:     make(chan struct{}),
	}

	section := sdp.VideoSection{
		Port:      session.port,
		Codec:     sdp.PayloadCodec{PayloadType: pt, Codec: sdp.Codec{Name: codec.Name, ClockRate: codec.ClockRate, Channels: 1}, Fmtp: fmtp},
		RTCPFbPLI: true,
	}.String()

	base := opts.Base
	if base == "" {
		base = fmt.Sprintf("v=0\r\no=tester 0 0 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\n", ip, ip)
	}

	session.offer = sdp.AppendSection(base, section)

	return session, nil
}

// videoPTFromOffer находит в m=video предложения MM payload type и fmtp нужного кодека
func videoPTFromOffer(offer string, codec VideoCodec) (int, string, error) {
	desc, err := sdp.Parse(offer)
	if err != nil {
		return 0, "", err
	}

	video, err := desc.Video()
	if err != nil {
		return 0, "", fmt.Errorf("rtp: в предложении MM %w", err)
	}

	pt, ok := findVideoPT(video, codec)
	if !ok {
		return 0, "", fmt.Errorf("rtp: в m=video предложения MM нет кодека %s (есть: %s)", codec.Name, videoCodecNames(video))
	}

	return pt, video.Fmtp[pt], nil
}

// findVideoPT — первый payload type раздела с кодеком codec
func findVideoPT(media *sdp.Media, codec VideoCodec) (int, bool) {
	for _, pt := range media.Payloads {
		if c, ok := media.Codecs[pt]; ok && strings.EqualFold(c.Name, codec.Name) {
			return pt, true
		}
	}

	return 0, false
}

func videoCodecNames(media *sdp.Media) string {
	var names []string

	for _, pt := range media.Payloads {
		if c, ok := media.Codecs[pt]; ok {
			names = append(names, fmt.Sprintf("%d %s", pt, c.Name))
		} else {
			names = append(names, strconv.Itoa(pt))
		}
	}

	if len(names) == 0 {
		return "ничего"
	}

	return strings.Join(names, ", ")
}

// listenPair занимает чётный порт из диапазона и следующий за ним (для RTCP)
func (f *Factory) listenPair(ip string) (*net.UDPConn, *net.UDPConn, error) {
	count := (f.portMax-f.portMin)/2 + 1

	for attempt := 0; attempt < count; attempt++ {
		rtpConn, err := f.listenFree(ip)
		if err != nil {
			return nil, nil, err
		}

		port := rtpConn.LocalAddr().(*net.UDPAddr).Port
		if port < 65535 {
			if rtcpConn, err := listenUDP(ip, port+1); err == nil {
				return rtpConn, rtcpConn, nil
			}
		}

		rtpConn.Close()
	}

	return nil, nil, fmt.Errorf("rtp: нет свободной пары портов RTP/RTCP в диапазоне %d-%d (адрес %s)", f.portMin, f.portMax, ip)
}

func (s *VideoSession) ID() string    { return s.id }
func (s *VideoSession) IP() string    { return s.ip }
func (s *VideoSession) Port() int     { return s.port }
func (s *VideoSession) RTCPPort() int { return s.port + 1 }
func (s *VideoSession) Offer() string { return s.offer }
func (s *VideoSession) PT() int       { return s.pt }
func (s *VideoSession) Codec() string { return s.codec.Name }

// Start берёт из SDP MM (ответа или предложения) адрес и порт видео и начинает приём RTP и RTCP,
// а если задан файл — и отправку видео из него.
func (s *VideoSession) Start(opts VideoStartOptions) error {
	desc, err := sdp.Parse(opts.RemoteSDP)
	if err != nil {
		return err
	}

	video, err := desc.Video()
	if err != nil {
		return fmt.Errorf("rtp: в SDP MM %w", err)
	}

	if video.Port == 0 {
		return errors.New("rtp: MM отклонил видео-поток (порт 0 в m=video)")
	}

	remotePT, ok := findVideoPT(video, s.codec)
	if !ok {
		return fmt.Errorf("rtp: в m=video MM нет кодека %s (есть: %s)", s.codec.Name, videoCodecNames(video))
	}

	host := opts.Host
	if host == "" {
		host = video.Addr
	}

	if host == "" || host == "0.0.0.0" {
		host = opts.DefaultHost
	}

	if host == "" || host == "0.0.0.0" {
		return ErrHostRequired
	}

	rtcpPort := video.RTCPPort
	if rtcpPort == 0 {
		rtcpPort = video.Port + 1
	}

	remoteRTCP, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(rtcpPort)))
	if err != nil {
		return fmt.Errorf("rtp: адрес RTCP %s:%d: %w", host, rtcpPort, err)
	}

	remoteRTP, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(video.Port)))
	if err != nil {
		return fmt.Errorf("rtp: адрес RTP %s:%d: %w", host, video.Port, err)
	}

	fps := opts.FPS
	if fps == 0 {
		fps = DefaultVideoFPS
	}

	if fps < 1 || fps > 60 {
		return fmt.Errorf("rtp: частота кадров %d вне диапазона 1-60", fps)
	}

	var stream *h264Stream

	if opts.File != "" {
		if s.codec.Name != "H264" {
			return fmt.Errorf("rtp: отправка из файла поддерживается только для H264, у сеанса %s", s.codec.Name)
		}

		if stream, err = loadH264(opts.File); err != nil {
			return fmt.Errorf("rtp: %w", err)
		}
	} else if opts.DropKeyframes {
		return errors.New("rtp: drop_keyframes имеет смысл только вместе с file")
	}

	// MM не принимает видео (sendonly/inactive в его SDP) — не шлём
	send := stream != nil && video.Direction != "sendonly" && video.Direction != "inactive"

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return errors.New("rtp: сеанс уже остановлен")
	}

	if s.started {
		return errors.New("rtp: сеанс уже запущен")
	}

	s.started = true
	s.startedAt = time.Now()
	s.remoteRTCP = remoteRTCP
	// MSR шлёт с payload type из SDP тестера; на всякий случай принимаем и номер из SDP MM
	s.acceptPT = map[uint8]bool{uint8(s.pt): true, uint8(remotePT): true}
	s.stats = VideoStats{
		Codec:  s.codec.Name,
		Local:  s.rtpConn.LocalAddr().String(),
		Remote: remoteRTP.String(),
	}

	s.wg.Add(2)
	go s.readLoop(s.rtpConn, s.handleRTP)
	go s.readLoop(s.rtcpConn, s.handleRTCP)

	if send {
		s.wg.Add(1)
		// шлём с payload type из SDP MM: получатель ждёт номер, который сам объявил
		go s.sendLoop(stream, fps, opts.DropKeyframes, uint8(remotePT), remoteRTP)
	}

	return nil
}

// sendLoop отправляет кадры файла по кругу с частотой fps. На PLI переходит к ближайшему опорному кадру файла
// и отправляет его (даже при drop_keyframes): дальше файл идёт с этого места, поэтому декодер получает целую цепочку.
func (s *VideoSession) sendLoop(stream *h264Stream, fps int, dropKeys bool, pt uint8, remote *net.UDPAddr) {
	defer s.wg.Done()

	interval := time.Second / time.Duration(fps)
	tsStep := uint32(90000 / fps)
	seq := uint16(rand.Uint32())
	ts := rand.Uint32()
	pos := 0
	next := time.Now()
	packet := make([]byte, headerLen+maxVideoPayload)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
		}

		s.mu.Lock()
		onPLI := s.keyRequest
		s.keyRequest = false
		s.mu.Unlock()

		if onPLI {
			pos = stream.nextKey(pos)
		}

		au := stream.aus[pos]
		pos = (pos + 1) % len(stream.aus)

		if au.key && dropKeys && !onPLI {
			s.mu.Lock()
			s.stats.DroppedKeyframes++
			s.mu.Unlock()
		} else {
			sent, ok := s.sendAU(au, packet, pt, &seq, ts, remote)

			s.mu.Lock()
			s.stats.SentPackets += uint64(sent)

			if ok {
				s.stats.SentFrames++

				if au.key {
					s.stats.SentKeyframes++

					if onPLI {
						s.stats.KeyframesOnPLI++
					}
				}
			}
			s.mu.Unlock()
		}

		ts += tsStep

		next = next.Add(interval)
		if now := time.Now(); now.Sub(next) > behindLimit {
			next = now
		}

		timer.Reset(time.Until(next))
	}
}

// sendAU отправляет кадр; marker ставится на последнем пакете кадра. Возвращает число отправленных пакетов
// и признак, что кадр ушёл целиком.
func (s *VideoSession) sendAU(au h264AU, packet []byte, pt uint8, seq *uint16, ts uint32, remote *net.UDPAddr) (int, bool) {
	sent := 0

	for i, nal := range au.nals {
		payloads := packetizeH264(nal, maxVideoPayload)

		for j, payload := range payloads {
			last := i == len(au.nals)-1 && j == len(payloads)-1

			Header{Marker: last, PayloadType: pt, Seq: *seq, Timestamp: ts, SSRC: s.ssrc}.put(packet)
			n := copy(packet[headerLen:], payload)
			*seq++

			if _, err := s.rtpConn.WriteToUDP(packet[:headerLen+n], remote); err != nil {
				return sent, false
			}

			sent++
		}
	}

	return sent, true
}

func (s *VideoSession) readLoop(conn *net.UDPConn, handle func([]byte, time.Time)) {
	defer s.wg.Done()

	buf := make([]byte, 2048)

	for {
		n, _, err := conn.ReadFromUDP(buf)
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

		handle(buf[:n], time.Now())
	}
}

// handleRTP учитывает видео-пакет: номера, кадры, опорные кадры и ответы на PLI
func (s *VideoSession) handleRTP(data []byte, arrival time.Time) {
	header, payload, err := ParsePacket(data)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.acceptPT[header.PayloadType] {
		s.stats.OtherPayloadPackets++
		return
	}

	s.stats.ReceivedPackets++
	s.stats.ReceivedBytes += uint64(len(data))

	if !s.haveSSRC || header.SSRC != s.remoteSSRC {
		// новый источник (или первый): счёт номеров заново
		s.haveSSRC = true
		s.remoteSSRC = header.SSRC
		s.haveSeq = false
	}

	s.trackSeq(header.Seq)

	if !s.haveTS || header.Timestamp != s.lastTS {
		s.haveTS = true
		s.lastTS = header.Timestamp
		s.stats.Frames++
	}

	if !IsVideoKeyframe(s.codec.Name, payload) {
		return
	}

	if s.haveKeyTS && header.Timestamp == s.lastKeyTS {
		return // продолжение того же опорного кадра
	}

	s.haveKeyTS = true
	s.lastKeyTS = header.Timestamp
	s.lastKeyAt = arrival
	s.stats.Keyframes++

	if !s.pendingPLI.IsZero() {
		s.lastPLIToKF = arrival.Sub(s.pendingPLI)
		s.stats.PLIAnswered++
		s.pendingPLI = time.Time{}
	}
}

// trackSeq ведёт расширенный номер пакета для подсчёта потерь (упрощённо, без учёта перестановок)
func (s *VideoSession) trackSeq(seq uint16) {
	if !s.haveSeq {
		s.haveSeq = true
		s.baseSeq = uint32(seq)
		s.maxSeq = uint32(seq)

		return
	}

	cycles := s.maxSeq &^ 0xFFFF
	ext := cycles | uint32(seq)

	// переход через 65535
	if ext+0x8000 < s.maxSeq {
		ext += 0x10000
	}

	if ext > s.maxSeq {
		s.maxSeq = ext
	}
}

// handleRTCP считает RTCP-пакеты и PLI в них
func (s *VideoSession) handleRTCP(data []byte, _ time.Time) {
	pli, ok := CountPLI(data)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.stats.RTCPReceived++
	s.stats.PLIReceived += uint64(pli)

	if pli > 0 {
		s.keyRequest = true
	}
}

// SendPLI отправляет count запросов опорного кадра (RTCP RR + PLI) с паузой interval между ними.
// Пакеты уходят с RTCP-порта тестера на RTCP-порт MM. Возвращается, когда отправлены все.
func (s *VideoSession) SendPLI(count int, interval time.Duration) error {
	if count < 1 {
		return fmt.Errorf("rtp: число PLI должно быть не меньше 1, получено %d", count)
	}

	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return errors.New("rtp: видео-сеанс не запущен: сначала video_start")
	}

	remote := s.remoteRTCP
	s.mu.Unlock()

	for i := 0; i < count; i++ {
		if i > 0 && interval > 0 {
			select {
			case <-s.done:
				return errors.New("rtp: сеанс остановлен во время отправки PLI")
			case <-time.After(interval):
			}
		}

		s.mu.Lock()
		packet := BuildPLI(s.ssrc, s.remoteSSRC)
		s.mu.Unlock()

		if _, err := s.rtcpConn.WriteToUDP(packet, remote); err != nil {
			return fmt.Errorf("rtp: отправка PLI на %s: %w", remote, err)
		}

		s.mu.Lock()
		s.stats.PLISent++

		if s.pendingPLI.IsZero() {
			s.pendingPLI = time.Now()
		}
		s.mu.Unlock()
	}

	return nil
}

// Stats — текущие счётчики
func (s *VideoSession) Stats() VideoStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return s.final
	}

	return s.snapshot(time.Now())
}

func (s *VideoSession) snapshot(now time.Time) VideoStats {
	st := s.stats

	if s.haveSSRC {
		st.RemoteSSRC = fmt.Sprintf("0x%08X", s.remoteSSRC)
	}

	if s.haveSeq {
		expected := uint64(s.maxSeq-s.baseSeq) + 1
		if received := st.ReceivedPackets; expected > received {
			st.LostPackets = expected - received
		}
	}

	st.LastPLIToKeyframeMs = -1
	if st.PLIAnswered > 0 {
		st.LastPLIToKeyframeMs = round3(float64(s.lastPLIToKF.Microseconds()) / 1000)
	}

	st.SinceLastKeyframeMs = -1
	if !s.lastKeyAt.IsZero() {
		st.SinceLastKeyframeMs = round3(float64(now.Sub(s.lastKeyAt).Microseconds()) / 1000)
	}

	if s.started {
		st.DurationSec = round3(now.Sub(s.startedAt).Seconds())
	}

	return st
}

// Running — сеанс запущен и ещё не остановлен
func (s *VideoSession) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.started && !s.stopped
}

// Stop останавливает приём, освобождает оба порта и возвращает итоговые счётчики. Повторный вызов безопасен.
func (s *VideoSession) Stop() VideoStats {
	s.mu.Lock()

	if s.stopped {
		defer s.mu.Unlock()
		return s.final
	}

	s.stopped = true
	s.mu.Unlock()

	close(s.done)
	s.rtpConn.Close()
	s.rtcpConn.Close()
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.final = s.snapshot(time.Now())

	return s.final
}

// ===== Разбор видео и RTCP (без состояния, проверяются отдельно) =====

// IsVideoKeyframe сообщает, начинается ли в RTP-пакете опорный кадр
func IsVideoKeyframe(codec string, payload []byte) bool {
	switch strings.ToUpper(codec) {
	case "H264":
		return h264HasIDR(payload)
	case "VP8":
		return vp8IsKeyframe(payload)
	}

	return false
}

const (
	h264NalIDR  = 5
	h264StapA   = 24
	h264FuA     = 28
	h264FuB     = 29
	h264NalMask = 0x1F
)

// h264HasIDR ищет в пакете (RFC 6184) NAL типа 5 (IDR): одиночный NAL, внутри STAP-A или первый фрагмент FU-A/FU-B
func h264HasIDR(p []byte) bool {
	if len(p) < 1 {
		return false
	}

	switch nal := p[0] & h264NalMask; {
	case nal >= 1 && nal <= 23:
		return nal == h264NalIDR
	case nal == h264StapA:
		for i := 1; i+2 <= len(p); {
			size := int(binary.BigEndian.Uint16(p[i:]))
			i += 2

			if size == 0 || i+size > len(p) {
				return false
			}

			if p[i]&h264NalMask == h264NalIDR {
				return true
			}

			i += size
		}
	case nal == h264FuA || nal == h264FuB:
		// FU header: S E R Type; учитываем только начало фрагментированного NAL
		return len(p) >= 2 && p[1]&0x80 != 0 && p[1]&h264NalMask == h264NalIDR
	}

	return false
}

// vp8IsKeyframe разбирает дескриптор VP8 (RFC 7741): опорный кадр — бит P=0 в первом пакете кадра (S=1, PID=0)
func vp8IsKeyframe(p []byte) bool {
	if len(p) < 1 {
		return false
	}

	start := p[0]&0x10 != 0
	partition := p[0] & 0x07
	i := 1

	if p[0]&0x80 != 0 { // X: есть байт расширения
		if len(p) < 2 {
			return false
		}

		ext := p[1]
		i = 2

		if ext&0x80 != 0 { // I: PictureID, 7 или 15 бит
			if len(p) <= i {
				return false
			}

			if p[i]&0x80 != 0 {
				i += 2
			} else {
				i++
			}
		}

		if ext&0x40 != 0 { // L: TL0PICIDX
			i++
		}

		if ext&0x20 != 0 || ext&0x10 != 0 { // T или K: TID/KEYIDX
			i++
		}
	}

	if !start || partition != 0 || len(p) <= i {
		return false
	}

	return p[i]&0x01 == 0
}

const (
	rtcpRR   = 201
	rtcpPSFB = 206
	fmtPLI   = 1
)

// BuildPLI собирает составной RTCP-пакет: пустой RR (без блоков отчёта) и PLI (RFC 4585, PT=206, FMT=1).
// mediaSSRC — SSRC потока, в котором нужен опорный кадр (0, если поток ещё не приходил).
func BuildPLI(senderSSRC, mediaSSRC uint32) []byte {
	b := make([]byte, 8+12)

	b[0] = 2 << 6 // RR, RC=0
	b[1] = rtcpRR
	binary.BigEndian.PutUint16(b[2:], 1) // длина в 32-битных словах минус 1
	binary.BigEndian.PutUint32(b[4:], senderSSRC)

	b[8] = 2<<6 | fmtPLI
	b[9] = rtcpPSFB
	binary.BigEndian.PutUint16(b[10:], 2)
	binary.BigEndian.PutUint32(b[12:], senderSSRC)
	binary.BigEndian.PutUint32(b[16:], mediaSSRC)

	return b
}

// CountPLI разбирает составной RTCP-пакет и считает в нём PLI; ok=false, если в начале пакета нет RTCP.
// Обрезанный хвост составного пакета пропускается.
func CountPLI(data []byte) (pli int, ok bool) {
	for len(data) > 0 {
		if len(data) < 4 || data[0]>>6 != 2 {
			return pli, ok
		}

		size := (int(binary.BigEndian.Uint16(data[2:])) + 1) * 4
		if size > len(data) {
			return pli, ok
		}

		pt := data[1]
		if pt < 192 || pt > 223 { // не RTCP (например, RTP по ошибке пришёл на этот порт)
			return pli, ok
		}

		ok = true

		if pt == rtcpPSFB && data[0]&0x1F == fmtPLI {
			pli++
		}

		data = data[size:]
	}

	return pli, ok
}

// FormatVideoSummary — строка итогов для лога
func FormatVideoSummary(st VideoStats) string {
	parts := []string{
		fmt.Sprintf("принято %d (потеряно %d)", st.ReceivedPackets, st.LostPackets),
		fmt.Sprintf("кадров %d, опорных %d", st.Frames, st.Keyframes),
		fmt.Sprintf("PLI отправлено %d, отвечено %d", st.PLISent, st.PLIAnswered),
	}

	if st.LastPLIToKeyframeMs >= 0 {
		parts = append(parts, fmt.Sprintf("PLI → опорный кадр %.1f мс", st.LastPLIToKeyframeMs))
	}

	if st.PLIReceived > 0 || st.RTCPReceived > 0 {
		parts = append(parts, fmt.Sprintf("RTCP принято %d, из них PLI %d", st.RTCPReceived, st.PLIReceived))
	}

	if st.SentPackets > 0 || st.DroppedKeyframes > 0 {
		parts = append(parts, fmt.Sprintf("отправлено пакетов %d, кадров %d, опорных %d (на PLI %d, пропущено %d)",
			st.SentPackets, st.SentFrames, st.SentKeyframes, st.KeyframesOnPLI, st.DroppedKeyframes))
	}

	if st.OtherPayloadPackets > 0 {
		parts = append(parts, fmt.Sprintf("чужих payload type %d", st.OtherPayloadPackets))
	}

	return strings.Join(parts, ", ")
}
