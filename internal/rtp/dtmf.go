package rtp

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// DTMF по RFC 4733 (бывший RFC 2833): события передаются пакетами с отдельным payload type в том же RTP-потоке.
// Нагрузка — 4 байта: номер события, флаг конца E и громкость, длительность в единицах RTP-часов.

const dtmfDigits = "0123456789*#ABCD"

const (
	// DefaultDTMFPT — payload type telephone-event в SDP, если он не задан
	DefaultDTMFPT = 101

	// DefaultDTMFDurationMs — длительность одной цифры по умолчанию
	DefaultDTMFDurationMs = 160

	// DefaultDTMFGapMs — пауза между цифрами по умолчанию
	DefaultDTMFGapMs = 100

	// DefaultDTMFVolume — громкость события в -дБм0 (0..63)
	DefaultDTMFVolume = 10

	// dtmfEndRepeats — сколько раз передаётся пакет окончания события (RFC 4733, 2.5.1.4)
	dtmfEndRepeats = 3

	maxDTMFDurationMs = 10000
)

// dtmfEvent переводит символ в номер события: 0-9, * = 10, # = 11, A-D = 12-15
func dtmfEvent(ch rune) (byte, bool) {
	i := strings.IndexRune(dtmfDigits, []rune(strings.ToUpper(string(ch)))[0])
	if i < 0 {
		return 0, false
	}

	return byte(i), true
}

// dtmfDigit — обратное преобразование для принятых событий
func dtmfDigit(event byte) (byte, bool) {
	if int(event) >= len(dtmfDigits) {
		return 0, false
	}

	return dtmfDigits[event], true
}

// ParseDTMF проверяет строку цифр и переводит её в номера событий
func ParseDTMF(digits string) ([]byte, error) {
	if digits == "" {
		return nil, fmt.Errorf("нет цифр для отправки")
	}

	events := make([]byte, 0, len(digits))

	for _, ch := range digits {
		event, ok := dtmfEvent(ch)
		if !ok {
			return nil, fmt.Errorf("недопустимый символ %q в DTMF (допустимы 0-9 * # A-D)", ch)
		}

		events = append(events, event)
	}

	return events, nil
}

// dtmfJob — цифра, поставленная в очередь отправителю
type dtmfJob struct {
	event   byte
	volume  byte
	packets int           // сколько пакетов длится событие
	done    chan struct{} // закрывается, когда отправлен последний пакет окончания
}

// dtmfState — ход отправки одной цифры
type dtmfState struct {
	job      *dtmfJob
	startTS  uint32 // timestamp начала события: один на все его пакеты
	sent     int    // отправлено пакетов основной части
	total    int    // всего пакетов этого события
	endsSent int
	clock    uint32 // единиц RTP-часов за один пакет
}

func newDTMFState(job *dtmfJob, startTS, clockPerFrame uint32) *dtmfState {
	return &dtmfState{job: job, startTS: startTS, clock: clockPerFrame}
}

// fill записывает очередной пакет события в payload (4 байта), возвращает признак маркера (первый пакет события)
// и признак «событие закончено» (отправлен последний повтор пакета окончания)
func (st *dtmfState) fill(payload []byte) (marker, finished bool) {
	marker = st.total == 0
	st.total++

	var (
		duration uint32
		end      bool
	)

	if st.sent < st.job.packets-1 {
		st.sent++
		duration = uint32(st.sent) * st.clock
	} else {
		// последний пакет основной части и его повторы несут флаг E и полную длительность
		duration = uint32(st.job.packets) * st.clock
		end = true
		st.endsSent++
	}

	flags := st.job.volume & 0x3F
	if end {
		flags |= 0x80
	}

	payload[0] = st.job.event
	payload[1] = flags
	binary.BigEndian.PutUint16(payload[2:], uint16(min(duration, 0xFFFF)))

	return marker, end && st.endsSent >= dtmfEndRepeats
}

// parseEventPayload разбирает 4 байта события из принятого пакета
func parseEventPayload(payload []byte) (event byte, end bool, duration uint16, ok bool) {
	if len(payload) < 4 {
		return 0, false, 0, false
	}

	return payload[0], payload[1]&0x80 != 0, binary.BigEndian.Uint16(payload[2:]), true
}
