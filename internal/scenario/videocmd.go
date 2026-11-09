package scenario

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"tester_mm/internal/rtp"
)

// Видео-команды: video_open / video_start / video_pli / video_stats / video_stop.
// Разбор аргументов общий с rtp_* (rtpSpecs в rtpcmd.go), выполнение — здесь.

// maxPLICount — верхняя граница count у video_pli, чтобы опечатка не превратилась в поток запросов
const maxPLICount = 1000

func (executor *Executor) videoOpen(label string, cmd Command, options map[string]string) error {
	opts := rtp.VideoOpenOptions{IP: options["ip"], Offer: options["offer"]}

	if len(cmd.RTPArgs) == 1 {
		value, err := executor.exprParser.ResolveValue(cmd.RTPArgs[0])
		if err != nil {
			return fmt.Errorf("video_open: %w", err)
		}

		base, err := baseSDP(value)
		if err != nil {
			return fmt.Errorf("video_open: %w", err)
		}

		opts.Base = base
	}

	if v, ok := options["codec"]; ok {
		codec, err := rtp.ParseVideoCodec(v)
		if err != nil {
			return fmt.Errorf("video_open: codec: %w (поддерживаются: %s)", err, rtp.VideoSupportedList)
		}

		opts.Codec = codec
	}

	if v, ok := options["pt"]; ok {
		pt, err := strconv.Atoi(v)
		if err != nil || pt < 96 || pt > 127 {
			return fmt.Errorf("video_open: pt: ожидается динамический payload type 96-127, получено %q", v)
		}

		opts.PT = pt
	}

	session, err := executor.rtpFactory.OpenVideo(opts)
	if err != nil {
		return fmt.Errorf("video_open: %w", err)
	}

	if executor.videoSessions == nil {
		executor.videoSessions = map[string]*rtp.VideoSession{}
	}

	executor.videoSessions[session.ID()] = session

	handle, _ := json.Marshal(map[string]interface{}{
		"id":        session.ID(),
		"ip":        session.IP(),
		"port":      session.Port(),
		"rtcp_port": session.RTCPPort(),
		"codec":     session.Codec(),
		"pt":        session.PT(),
		"sdp":       session.Offer(),
	})

	executor.tracef("[%s] video_open: сеанс %s, %s/%d, RTP %s:%d, RTCP :%d", label, session.ID(), session.Codec(), session.PT(), session.IP(), session.Port(), session.RTCPPort())

	executor.exprParser.vars.Set(cmd.VarName, string(handle))
	executor.tracef("[%s] сеанс сохранён в переменную '%s' (поля id, ip, port, rtcp_port, codec, pt, sdp; sdp — SDP плеча с разделом m=video)", label, cmd.VarName)

	return nil
}

// baseSDP достаёт SDP, к которому добавляется видео: из описателя rtp_open (поле sdp) или сам текст SDP
func baseSDP(value string) (string, error) {
	if fromHandle, err := jsonFieldPath(value, []string{"sdp"}); err == nil {
		return fromHandle, nil
	}

	if strings.HasPrefix(strings.TrimSpace(value), "v=") {
		return value, nil
	}

	return "", fmt.Errorf("аргумент должен быть RTP-сеансом (rtp = rtp_open(...)) или текстом SDP")
}

func (executor *Executor) videoStart(label string, cmd Command, options map[string]string) error {
	session, err := executor.videoSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("video_start: %w", err)
	}

	remote, err := executor.exprParser.ResolveValue(cmd.RTPArgs[1])
	if err != nil {
		return fmt.Errorf("video_start: SDP: %w", err)
	}

	start := rtp.VideoStartOptions{
		RemoteSDP:   remote,
		Host:        options["host"],
		DefaultHost: executor.mmHost(),
		File:        options["file"],
	}

	if v, ok := options["fps"]; ok {
		if start.FPS, err = strconv.Atoi(v); err != nil || start.FPS < 1 || start.FPS > 60 {
			return fmt.Errorf("video_start: fps: ожидается число 1-60, получено %q", v)
		}
	}

	if v, ok := options["drop_keyframes"]; ok {
		switch v {
		case "true":
			start.DropKeyframes = true
		case "false":
		default:
			return fmt.Errorf("video_start: drop_keyframes: ожидается true или false, получено %q", v)
		}
	}

	if err := session.Start(start); err != nil {
		return fmt.Errorf("video_start: %w", err)
	}

	stats := session.Stats()
	executor.tracef("[%s] video_start: сеанс %s, %s, приём на %s, MM %s%s", label, session.ID(), stats.Codec, stats.Local, stats.Remote, videoStartNotes(start))

	return nil
}

func (executor *Executor) videoPLI(label string, cmd Command, options map[string]string) error {
	session, err := executor.videoSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("video_pli: %w", err)
	}

	count := 1
	if v, ok := options["count"]; ok {
		if count, err = strconv.Atoi(v); err != nil || count < 1 || count > maxPLICount {
			return fmt.Errorf("video_pli: count: ожидается число 1-%d, получено %q", maxPLICount, v)
		}
	}

	interval := 0
	if v, ok := options["interval"]; ok {
		if interval, err = strconv.Atoi(v); err != nil || interval < 0 {
			return fmt.Errorf("video_pli: interval: ожидается неотрицательное число миллисекунд, получено %q", v)
		}
	}

	executor.tracef("[%s] video_pli: сеанс %s, PLI × %d, пауза %d мс", label, session.ID(), count, interval)

	if err := session.SendPLI(count, time.Duration(interval)*time.Millisecond); err != nil {
		return fmt.Errorf("video_pli: %w", err)
	}

	return nil
}

// videoStartNotes описывает отправку для строки лога
func videoStartNotes(start rtp.VideoStartOptions) string {
	if start.File == "" {
		return " (только приём)"
	}

	fps := start.FPS
	if fps == 0 {
		fps = rtp.DefaultVideoFPS
	}

	note := fmt.Sprintf(" (шлёт %s, %d кадр/с", start.File, fps)
	if start.DropKeyframes {
		note += ", опорные кадры только в ответ на PLI"
	}

	return note + ")"
}

// videoFinish выполняет video_stats и video_stop: пишет итоги в лог и, если задана переменная, сохраняет их JSON
func (executor *Executor) videoFinish(label string, cmd Command) error {
	session, err := executor.videoSession(cmd.RTPArgs[0])
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.RTPOp, err)
	}

	var stats rtp.VideoStats

	if cmd.RTPOp == "video_stop" {
		stats = session.Stop()
	} else {
		stats = session.Stats()
	}

	executor.tracef("[%s] %s: сеанс %s, %s", label, cmd.RTPOp, session.ID(), rtp.FormatVideoSummary(stats))

	if cmd.VarName != "" {
		data, _ := json.Marshal(stats)
		executor.exprParser.vars.Set(cmd.VarName, string(data))
	}

	return nil
}

// videoSession находит видео-сеанс по переменной-описателю или по номеру
func (executor *Executor) videoSession(expr string) (*rtp.VideoSession, error) {
	value, err := executor.exprParser.ResolveValue(expr)
	if err != nil {
		return nil, err
	}

	id := value
	if fromHandle, err := jsonFieldPath(value, []string{"id"}); err == nil {
		id = fromHandle
	}

	session, ok := executor.videoSessions[id]
	if !ok {
		if _, isAudio := executor.rtpSessions[id]; isAudio {
			return nil, fmt.Errorf("%q — аудио-сеанс rtp_open, а нужен видео-сеанс: v = video_open(...)", id)
		}

		return nil, fmt.Errorf("видео-сеанс %q не найден: сначала выполните v = video_open(...)", id)
	}

	return session, nil
}

// closeVideo останавливает видео-сеансы, которые сценарий не остановил сам
func (executor *Executor) closeVideo() {
	ids := make([]string, 0, len(executor.videoSessions))
	for id := range executor.videoSessions {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		session := executor.videoSessions[id]

		if session.Running() {
			executor.tracef("видео-сеанс %s не остановлен в сценарии: останавливаем (добавьте video_stop, чтобы получить итоги)", id)
		}

		session.Stop()
	}

	executor.videoSessions = nil
}
