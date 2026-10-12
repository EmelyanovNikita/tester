// Package rtp отправляет и принимает RTP-поток по UDP: заголовок, кодеки G.711, тон и запись в WAV.
package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const headerLen = 12

// Header — фиксированная часть RTP-заголовка (RFC 3550)
type Header struct {
	Marker      bool
	PayloadType uint8
	Seq         uint16
	Timestamp   uint32
	SSRC        uint32
}

// put записывает заголовок (версия 2, без CSRC и расширений) в первые 12 байт b
func (h Header) put(b []byte) {
	b[0] = 2 << 6

	b[1] = h.PayloadType & 0x7F
	if h.Marker {
		b[1] |= 0x80
	}

	binary.BigEndian.PutUint16(b[2:], h.Seq)
	binary.BigEndian.PutUint32(b[4:], h.Timestamp)
	binary.BigEndian.PutUint32(b[8:], h.SSRC)
}

var errNotRTP = errors.New("не RTP-пакет")

// ParsePacket разбирает пакет и возвращает заголовок и полезную нагрузку.
// CSRC, расширение заголовка и padding учитываются, чтобы нагрузка выделялась правильно.
func ParsePacket(b []byte) (Header, []byte, error) {
	if len(b) < headerLen {
		return Header{}, nil, fmt.Errorf("%w: %d байт, меньше заголовка", errNotRTP, len(b))
	}

	if b[0]>>6 != 2 {
		return Header{}, nil, fmt.Errorf("%w: версия %d", errNotRTP, b[0]>>6)
	}

	header := Header{
		Marker:      b[1]&0x80 != 0,
		PayloadType: b[1] & 0x7F,
		Seq:         binary.BigEndian.Uint16(b[2:]),
		Timestamp:   binary.BigEndian.Uint32(b[4:]),
		SSRC:        binary.BigEndian.Uint32(b[8:]),
	}

	offset := headerLen + 4*int(b[0]&0x0F)
	if len(b) < offset {
		return Header{}, nil, fmt.Errorf("%w: обрезан список CSRC", errNotRTP)
	}

	if b[0]&0x10 != 0 {
		if len(b) < offset+4 {
			return Header{}, nil, fmt.Errorf("%w: обрезано расширение", errNotRTP)
		}

		offset += 4 + 4*int(binary.BigEndian.Uint16(b[offset+2:]))
		if len(b) < offset {
			return Header{}, nil, fmt.Errorf("%w: обрезано расширение", errNotRTP)
		}
	}

	end := len(b)

	if b[0]&0x20 != 0 {
		pad := int(b[len(b)-1])
		if pad == 0 || pad > end-offset {
			return Header{}, nil, fmt.Errorf("%w: неверный padding", errNotRTP)
		}

		end -= pad
	}

	return header, b[offset:end], nil
}
