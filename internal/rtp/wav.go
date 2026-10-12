package rtp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
)

const wavHeaderLen = 44

// wavWriter пишет WAV, 16 бит, моно или стерео (отсчёты каналов чередуются). Размеры в заголовке дописываются при закрытии.
type wavWriter struct {
	file     *os.File
	out      *bufio.Writer
	rate     int
	channels int
	dataLen  uint32
}

func createWAV(path string, sampleRate, channels int) (*wavWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("создание WAV: %w", err)
	}

	w := &wavWriter{file: file, out: bufio.NewWriter(file), rate: sampleRate, channels: channels}

	if _, err := w.out.Write(make([]byte, wavHeaderLen)); err != nil {
		file.Close()
		return nil, fmt.Errorf("запись WAV: %w", err)
	}

	return w, nil
}

func (w *wavWriter) write(samples []int16) error {
	var buf [2]byte

	for _, s := range samples {
		binary.LittleEndian.PutUint16(buf[:], uint16(s))

		if _, err := w.out.Write(buf[:]); err != nil {
			return err
		}

		w.dataLen += 2
	}

	return nil
}

func (w *wavWriter) close() error {
	if err := w.out.Flush(); err != nil {
		w.file.Close()
		return err
	}

	header := make([]byte, wavHeaderLen)

	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+w.dataLen)
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)                          // размер блока fmt
	binary.LittleEndian.PutUint16(header[20:], 1)                           // PCM
	binary.LittleEndian.PutUint16(header[22:], uint16(w.channels))          // каналов
	binary.LittleEndian.PutUint32(header[24:], uint32(w.rate))              // частота
	binary.LittleEndian.PutUint32(header[28:], uint32(w.rate*w.channels*2)) // байт в секунду
	binary.LittleEndian.PutUint16(header[32:], uint16(w.channels*2))        // байт на отсчёт
	binary.LittleEndian.PutUint16(header[34:], 16)                          // бит на отсчёт
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], w.dataLen)

	if _, err := w.file.WriteAt(header, 0); err != nil {
		w.file.Close()
		return err
	}

	return w.file.Close()
}
