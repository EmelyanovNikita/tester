package sdp

import (
	"strings"
	"testing"
)

// Ответ MM на ChannelAdd из реального прогона
const realAnswer = "v=0\r\no=- 560971606 560971606 IN IP4 127.0.0.1\r\ns=\r\nt=0 0\r\nm=audio 12352 RTP/AVP 0\r\nc=IN IP4 0.0.0.0\r\nb=AS:62\r\na=sendrecv\r\na=rtpmap:0 PCMU/8000/1\r\na=ptime:10\r\n"

func TestParseRealAnswer(t *testing.T) {
	desc, err := Parse(realAnswer)
	if err != nil {
		t.Fatal(err)
	}

	audio, err := desc.Audio()
	if err != nil {
		t.Fatal(err)
	}

	if audio.Port != 12352 || audio.Addr != "0.0.0.0" || audio.PTime != 10 || audio.Direction != "sendrecv" {
		t.Errorf("неверно разобран раздел: %+v", audio)
	}

	if len(audio.Payloads) != 1 || audio.Payloads[0] != 0 {
		t.Errorf("payloads: %v", audio.Payloads)
	}

	if c := audio.Codecs[0]; c.Name != "PCMU" || c.ClockRate != 8000 || c.Channels != 1 {
		t.Errorf("кодек: %+v", c)
	}
}

func TestSessionLevelAndOverrides(t *testing.T) {
	text := "v=0\r\nc=IN IP4 10.0.0.1\r\na=recvonly\r\nt=0 0\r\n" +
		"m=audio 4000 RTP/AVP 8 0\r\n" +
		"m=video 5000 RTP/AVP 96\r\nc=IN IP4 10.0.0.2\r\na=sendonly\r\na=rtpmap:96 H264/90000\r\n"

	desc, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := desc.Audio()
	if audio.Addr != "10.0.0.1" || audio.Direction != "recvonly" {
		t.Errorf("сессионные c= и направление не применились: %+v", audio)
	}

	// статические кодеки известны и без rtpmap
	if audio.Codecs[8].Name != "PCMA" || audio.Codecs[0].Name != "PCMU" {
		t.Errorf("статические кодеки: %+v", audio.Codecs)
	}

	if audio.Payloads[0] != 8 {
		t.Errorf("порядок payload должен сохраняться: %v", audio.Payloads)
	}

	video := desc.Media[1]
	if video.Addr != "10.0.0.2" || video.Direction != "sendonly" || video.Codecs[96].Name != "H264" {
		t.Errorf("video: %+v", video)
	}
}

func TestParseErrorsAndTolerance(t *testing.T) {
	if _, err := Parse("m=audio abc RTP/AVP 0\r\n"); err == nil {
		t.Error("неверный порт должен давать ошибку")
	}

	if _, err := Parse("m=audio 100\r\n"); err == nil {
		t.Error("короткая строка m= должна давать ошибку")
	}

	desc, err := Parse("мусор\r\nx\r\n\r\nm=audio 100 RTP/AVP 0\r\na=rtpmap:bad\r\na=ptime:zzz\r\n")
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := desc.Audio()
	if audio.PTime != 0 || audio.Addr != "" {
		t.Errorf("некорректные атрибуты должны игнорироваться: %+v", audio)
	}

	empty, _ := Parse("v=0\r\n")
	if _, err := empty.Audio(); err == nil {
		t.Error("без m=audio Audio() должен возвращать ошибку")
	}
}

func TestOfferRoundTrip(t *testing.T) {
	offer := Offer{
		IP:   "192.168.1.5",
		Port: 20000,
		Codecs: []PayloadCodec{
			{PayloadType: 0, Codec: Codec{"PCMU", 8000, 1}},
			{PayloadType: 8, Codec: Codec{"PCMA", 8000, 1}},
		},
		PTime: 20,
	}

	text := offer.String()
	if !strings.Contains(text, "\r\n") || !strings.Contains(text, "m=audio 20000 RTP/AVP 0 8\r\n") {
		t.Fatalf("неожиданный SDP:\n%s", text)
	}

	desc, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := desc.Audio()
	if audio.Port != 20000 || audio.Addr != "192.168.1.5" || audio.PTime != 20 ||
		len(audio.Payloads) != 2 || audio.Codecs[8].Name != "PCMA" {
		t.Errorf("после разбора: %+v", audio)
	}
}
