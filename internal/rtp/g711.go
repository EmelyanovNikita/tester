package rtp

// ===== G.711 μ-law =====

const (
	ulawBias = 0x84
	ulawClip = 32635
)

func encodeULaw(sample int16) byte {
	value := int(sample)
	sign := 0

	if value < 0 {
		value = -value
		sign = 0x80
	}

	if value > ulawClip {
		value = ulawClip
	}

	value += ulawBias

	exponent := 7
	for mask := 0x4000; value&mask == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}

	mantissa := (value >> (exponent + 3)) & 0x0F

	return ^byte(sign | exponent<<4 | mantissa)
}

func decodeULaw(code byte) int16 {
	code = ^code

	exponent := int(code>>4) & 0x07
	mantissa := int(code & 0x0F)

	value := ((mantissa<<3 + ulawBias) << exponent) - ulawBias

	if code&0x80 != 0 {
		return int16(-value)
	}

	return int16(value)
}

// ===== G.711 A-law =====

var alawSegEnd = [8]int{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func encodeALaw(sample int16) byte {
	value := int(sample) >> 3

	var mask byte

	if value >= 0 {
		mask = 0xD5
	} else {
		mask = 0x55
		value = -value - 1
	}

	segment := 0
	for segment < 8 && value > alawSegEnd[segment] {
		segment++
	}

	if segment >= 8 {
		return 0x7F ^ mask
	}

	code := byte(segment << 4)

	if segment < 2 {
		code |= byte(value>>1) & 0x0F
	} else {
		code |= byte(value>>segment) & 0x0F
	}

	return code ^ mask
}

func decodeALaw(code byte) int16 {
	code ^= 0x55

	value := int(code&0x0F) << 4
	segment := int(code&0x70) >> 4

	switch segment {
	case 0:
		value += 8
	case 1:
		value += 0x108
	default:
		value += 0x108
		value <<= segment - 1
	}

	if code&0x80 != 0 {
		return int16(value)
	}

	return int16(-value)
}
