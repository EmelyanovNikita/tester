package rtp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Видео из файла H.264 в формате Annex B (поток NAL-единиц со стартовыми кодами 00 00 01 / 00 00 00 01),
// как его пишет ffmpeg: ffmpeg -i in.mp4 -an -c:v libx264 -profile:v baseline -bf 0 -g 250 video.h264

const (
	// maxVideoFileSize — больше файл не читается целиком в память
	maxVideoFileSize = 64 << 20

	// maxVideoPayload — наибольшая нагрузка RTP-пакета; NAL длиннее режется на фрагменты FU-A
	maxVideoPayload = 1200

	h264NalSEI = 6
	h264NalSPS = 7
	h264NalPPS = 8
	h264NalAUD = 9
)

// h264AU — кадр (access unit): NAL-единицы с одним RTP timestamp
type h264AU struct {
	nals [][]byte
	key  bool // есть NAL IDR
}

// h264Stream — разобранный файл: кадры по порядку и номера опорных кадров среди них
type h264Stream struct {
	aus  []h264AU
	keys []int
}

var h264Cache = struct {
	sync.Mutex
	m map[string]*h264Stream
}{m: map[string]*h264Stream{}}

// loadH264 читает и разбирает файл; разобранный файл кэшируется, чтобы потоки нагрузки не читали его заново
func loadH264(path string) (*h264Stream, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	h264Cache.Lock()
	defer h264Cache.Unlock()

	if stream, ok := h264Cache.m[abs]; ok {
		return stream, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("видеофайл: %w", err)
	}

	if info.Size() > maxVideoFileSize {
		return nil, fmt.Errorf("видеофайл %s: %d байт, больше предела %d", path, info.Size(), maxVideoFileSize)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("видеофайл: %w", err)
	}

	stream, err := parseH264Stream(data)
	if err != nil {
		return nil, fmt.Errorf("видеофайл %s: %w (нужен H.264 Annex B: ffmpeg -i in.mp4 -an -c:v libx264 -profile:v baseline -bf 0 -g 250 video.h264)", path, err)
	}

	h264Cache.m[abs] = stream

	return stream, nil
}

func parseH264Stream(data []byte) (*h264Stream, error) {
	nals := splitAnnexB(data)
	if len(nals) == 0 {
		return nil, fmt.Errorf("не найдено ни одной NAL-единицы со стартовым кодом")
	}

	stream := &h264Stream{aus: groupAccessUnits(nals)}
	if len(stream.aus) == 0 {
		return nil, fmt.Errorf("нет кадров (NAL-единиц со срезами изображения)")
	}

	for i, au := range stream.aus {
		if au.key {
			stream.keys = append(stream.keys, i)
		}
	}

	if len(stream.keys) == 0 {
		return nil, fmt.Errorf("нет ни одного опорного кадра (IDR)")
	}

	return stream, nil
}

// splitAnnexB делит поток по стартовым кодам; нули перед следующим стартовым кодом (00 00 00 01) отбрасываются
func splitAnnexB(data []byte) [][]byte {
	var nals [][]byte

	start := -1

	for i := 0; i+2 < len(data); {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if start >= 0 {
				nals = appendNAL(nals, data[start:i])
			}

			i += 3
			start = i

			continue
		}

		i++
	}

	if start >= 0 {
		nals = appendNAL(nals, data[start:])
	}

	return nals
}

func appendNAL(nals [][]byte, nal []byte) [][]byte {
	end := len(nal)
	for end > 0 && nal[end-1] == 0 {
		end--
	}

	if end == 0 {
		return nals
	}

	return append(nals, nal[:end])
}

// groupAccessUnits собирает NAL-единицы в кадры (упрощённо по H.264 7.4.1.2.3): новый кадр начинают AUD, SPS, PPS,
// SEI и срез с first_mb_in_slice = 0, если в текущем кадре срез уже был. Кадры без срезов отбрасываются.
func groupAccessUnits(nals [][]byte) []h264AU {
	var (
		aus     []h264AU
		current h264AU
		hasVCL  bool
	)

	flush := func() {
		if hasVCL {
			aus = append(aus, current)
		}

		current, hasVCL = h264AU{}, false
	}

	for _, nal := range nals {
		nalType := nal[0] & h264NalMask
		vcl := nalType == 1 || nalType == h264NalIDR

		if hasVCL {
			switch {
			case nalType == h264NalAUD, nalType == h264NalSPS, nalType == h264NalPPS, nalType == h264NalSEI:
				flush()
			case vcl && len(nal) > 1 && nal[1]&0x80 != 0: // ue(v) first_mb_in_slice == 0 — первый бит 1
				flush()
			}
		}

		current.nals = append(current.nals, nal)

		if nalType == h264NalIDR {
			current.key = true
		}

		if vcl {
			hasVCL = true
		}
	}

	flush()

	return aus
}

// packetizeH264 упаковывает NAL по RFC 6184 (packetization-mode=1): целиком, если помещается, иначе фрагментами FU-A
func packetizeH264(nal []byte, maxPayload int) [][]byte {
	if len(nal) <= maxPayload {
		return [][]byte{nal}
	}

	indicator := nal[0]&0xE0 | h264FuA
	nalType := nal[0] & h264NalMask
	body := nal[1:]
	chunk := maxPayload - 2

	var payloads [][]byte

	for offset := 0; offset < len(body); offset += chunk {
		end := offset + chunk
		if end > len(body) {
			end = len(body)
		}

		header := nalType
		if offset == 0 {
			header |= 0x80 // S
		}

		if end == len(body) {
			header |= 0x40 // E
		}

		payload := make([]byte, 0, 2+end-offset)
		payload = append(payload, indicator, header)
		payload = append(payload, body[offset:end]...)
		payloads = append(payloads, payload)
	}

	return payloads
}

// nextKey — номер ближайшего опорного кадра начиная с pos (по кругу)
func (stream *h264Stream) nextKey(pos int) int {
	for _, k := range stream.keys {
		if k >= pos {
			return k
		}
	}

	return stream.keys[0]
}
