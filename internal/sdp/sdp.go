// Package sdp разбирает и собирает ту часть SDP, которая нужна тестеру для RTP:
// адрес и порт аудио, список кодеков, ptime и направление.
package sdp

import (
	"fmt"
	"strconv"
	"strings"
)

// Codec — кодек из a=rtpmap (или статический по номеру payload type)
type Codec struct {
	Name      string // PCMU, PCMA, ...
	ClockRate int
	Channels  int
}

// Media — один раздел m= с учётом сессионных c= и a=
type Media struct {
	Type     string // audio, video, ...
	Port     int
	Proto    string
	Payloads []int // payload type в порядке предпочтения
	Codecs   map[int]Codec
	Addr     string // адрес из c=: медиа-уровня, иначе сессионного; пусто, если его нет
	PTime    int    // a=ptime, 0 если не задан
	// Direction: sendrecv (по умолчанию), sendonly, recvonly, inactive
	Direction string
}

// Description — разобранный SDP
type Description struct {
	Media []Media
}

// Статические payload type (RFC 3551), для которых rtpmap можно не указывать
var staticCodecs = map[int]Codec{
	0:  {"PCMU", 8000, 1},
	3:  {"GSM", 8000, 1},
	4:  {"G723", 8000, 1},
	8:  {"PCMA", 8000, 1},
	9:  {"G722", 8000, 1}, // в SDP частота G.722 записывается как 8000
	18: {"G729", 8000, 1},
}

var directions = map[string]bool{"sendrecv": true, "sendonly": true, "recvonly": true, "inactive": true}

// Parse разбирает SDP. Незнакомые и некорректные строки пропускаются: нужные поля проверяются при использовании.
func Parse(text string) (*Description, error) {
	desc := &Description{}

	var (
		sessionAddr string
		sessionDir  string
		current     *Media
		mediaAddr   []string // c= внутри каждого раздела
		mediaDir    []string
	)

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if len(line) < 2 || line[1] != '=' {
			continue
		}

		key, value := line[0], line[2:]

		switch key {
		case 'm':
			media, err := parseMediaLine(value)
			if err != nil {
				return nil, err
			}

			desc.Media = append(desc.Media, media)
			mediaAddr = append(mediaAddr, "")
			mediaDir = append(mediaDir, "")
			current = &desc.Media[len(desc.Media)-1]
		case 'c':
			addr := parseConnection(value)

			if current == nil {
				sessionAddr = addr
			} else {
				mediaAddr[len(mediaAddr)-1] = addr
			}
		case 'a':
			name, arg, _ := strings.Cut(value, ":")
			name = strings.TrimSpace(name)

			switch {
			case directions[name]:
				if current == nil {
					sessionDir = name
				} else {
					mediaDir[len(mediaDir)-1] = name
				}
			case name == "rtpmap" && current != nil:
				if pt, codec, ok := parseRtpmap(arg); ok {
					current.Codecs[pt] = codec
				}
			case name == "ptime" && current != nil:
				if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && n > 0 {
					current.PTime = n
				}
			}
		}
	}

	for i := range desc.Media {
		media := &desc.Media[i]

		media.Addr = mediaAddr[i]
		if media.Addr == "" {
			media.Addr = sessionAddr
		}

		media.Direction = mediaDir[i]
		if media.Direction == "" {
			media.Direction = sessionDir
		}

		if media.Direction == "" {
			media.Direction = "sendrecv"
		}

		for _, pt := range media.Payloads {
			if _, ok := media.Codecs[pt]; !ok {
				if codec, ok := staticCodecs[pt]; ok {
					media.Codecs[pt] = codec
				}
			}
		}
	}

	return desc, nil
}

// parseMediaLine разбирает "audio 12352 RTP/AVP 0 8"
func parseMediaLine(value string) (Media, error) {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return Media{}, fmt.Errorf("sdp: неверная строка m=%s", value)
	}

	// порт может быть записан как "12352/2" (число портов)
	portText, _, _ := strings.Cut(fields[1], "/")

	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return Media{}, fmt.Errorf("sdp: неверный порт в строке m=%s", value)
	}

	media := Media{
		Type:   fields[0],
		Port:   port,
		Proto:  fields[2],
		Codecs: map[int]Codec{},
	}

	for _, format := range fields[3:] {
		if pt, err := strconv.Atoi(format); err == nil {
			media.Payloads = append(media.Payloads, pt)
		}
	}

	return media, nil
}

// parseConnection достаёт адрес из "IN IP4 1.2.3.4" (с необязательным "/ttl")
func parseConnection(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return ""
	}

	addr, _, _ := strings.Cut(fields[2], "/")

	return addr
}

// parseRtpmap разбирает "0 PCMU/8000" и "111 opus/48000/2"
func parseRtpmap(arg string) (int, Codec, bool) {
	ptText, rest, ok := strings.Cut(strings.TrimSpace(arg), " ")
	if !ok {
		return 0, Codec{}, false
	}

	pt, err := strconv.Atoi(ptText)
	if err != nil {
		return 0, Codec{}, false
	}

	parts := strings.Split(strings.TrimSpace(rest), "/")

	codec := Codec{Name: parts[0], Channels: 1}
	if len(parts) < 2 {
		return 0, Codec{}, false
	}

	rate, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, Codec{}, false
	}

	codec.ClockRate = rate

	if len(parts) > 2 {
		if ch, err := strconv.Atoi(parts[2]); err == nil && ch > 0 {
			codec.Channels = ch
		}
	}

	return pt, codec, true
}

// Audio возвращает первый раздел audio
func (desc *Description) Audio() (*Media, error) {
	for i := range desc.Media {
		if desc.Media[i].Type == "audio" {
			return &desc.Media[i], nil
		}
	}

	return nil, fmt.Errorf("sdp: нет раздела m=audio")
}

// PayloadCodec — кодек вместе с номером payload type для SDP
type PayloadCodec struct {
	PayloadType int
	Codec
}

// Offer — параметры аудио-предложения, которое собирает тестер
type Offer struct {
	IP     string
	Port   int
	Codecs []PayloadCodec
	PTime  int
}

// String собирает SDP в формате с CRLF
func (offer Offer) String() string {
	var b strings.Builder

	payloads := make([]string, len(offer.Codecs))
	for i, c := range offer.Codecs {
		payloads[i] = strconv.Itoa(c.PayloadType)
	}

	fmt.Fprintf(&b, "v=0\r\n")
	fmt.Fprintf(&b, "o=tester 0 0 IN IP4 %s\r\n", offer.IP)
	fmt.Fprintf(&b, "s=-\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", offer.IP)
	fmt.Fprintf(&b, "t=0 0\r\n")
	fmt.Fprintf(&b, "m=audio %d RTP/AVP %s\r\n", offer.Port, strings.Join(payloads, " "))

	for _, c := range offer.Codecs {
		fmt.Fprintf(&b, "a=rtpmap:%d %s/%d\r\n", c.PayloadType, c.Name, c.ClockRate)
	}

	if offer.PTime > 0 {
		fmt.Fprintf(&b, "a=ptime:%d\r\n", offer.PTime)
	}

	fmt.Fprintf(&b, "a=sendrecv\r\n")

	return b.String()
}
