package rtp

// G.722 (ITU-T G.722, режим 1: 64 кбит/с, звук 16 кГц): два поддиапазона с ADPCM, каждый отсчёт — 4 бита на нижнюю
// полосу и 2 на верхнюю; на выходе 1 байт на 2 отсчёта. В SDP частота G.722 записана как 8000 (историческая ошибка
// RFC 3551), хотя звук идёт на 16000 Гц.

var g722QMFCoeffs = [12]int{3, -11, 12, 32, -210, 951, 3876, -805, 362, -156, 53, -11}

var g722Q6 = [32]int{
	0, 35, 72, 110, 150, 190, 233, 276, 323, 370, 422, 473, 530, 587, 650, 714,
	786, 858, 940, 1023, 1121, 1219, 1339, 1458, 1612, 1765, 1980, 2195, 2557, 2919, 0, 0,
}

var g722ILN = [32]int{
	0, 63, 62, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19,
	18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 0,
}

var g722ILP = [32]int{
	0, 61, 60, 59, 58, 57, 56, 55, 54, 53, 52, 51, 50, 49, 48, 47,
	46, 45, 44, 43, 42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 32, 0,
}

var g722WL = [8]int{-60, -30, 58, 172, 334, 538, 1198, 3042}

var g722RL42 = [16]int{0, 7, 6, 5, 4, 3, 2, 1, 7, 6, 5, 4, 3, 2, 1, 0}

var g722ILB = [32]int{
	2048, 2093, 2139, 2186, 2233, 2282, 2332, 2383, 2435, 2489, 2543, 2599, 2656, 2714, 2774, 2834,
	2896, 2960, 3025, 3091, 3158, 3228, 3298, 3371, 3444, 3520, 3597, 3676, 3756, 3838, 3922, 4008,
}

var g722QM4 = [16]int{
	0, -20456, -12896, -8968, -6288, -4240, -2584, -1200,
	20456, 12896, 8968, 6288, 4240, 2584, 1200, 0,
}

var g722QM2 = [4]int{-7408, -1616, 7408, 1616}

var g722QM6 = [64]int{
	-136, -136, -136, -136, -24808, -21904, -19008, -16704,
	-14984, -13512, -12280, -11192, -10232, -9360, -8576, -7856,
	-7192, -6576, -6000, -5456, -4944, -4464, -4008, -3576,
	-3168, -2776, -2400, -2032, -1688, -1360, -1040, -728,
	24808, 21904, 19008, 16704, 14984, 13512, 12280, 11192,
	10232, 9360, 8576, 7856, 7192, 6576, 6000, 5456,
	4944, 4464, 4008, 3576, 3168, 2776, 2400, 2032,
	1688, 1360, 1040, 728, 432, 136, -432, -136,
}

var g722WH = [3]int{0, -214, 798}

var g722RH2 = [4]int{2, 1, 2, 1}

var g722IHN = [3]int{0, 1, 0}

var g722IHP = [3]int{0, 3, 2}

func g722Saturate(x int) int {
	if x > 32767 {
		return 32767
	}

	if x < -32768 {
		return -32768
	}

	return x
}

// g722Band — состояние адаптивного предсказателя одной полосы
type g722Band struct {
	s, sp, sz int
	r, a, ap  [3]int
	p         [3]int
	d, b, bp  [7]int
	sg        [7]int
	nb, det   int
}

func newG722Bands() [2]g722Band {
	var bands [2]g722Band

	bands[0].det = 32
	bands[1].det = 8

	return bands
}

// adapt обновляет предсказатель по очередной разности d (блок 4 рекомендации)
func (band *g722Band) adapt(d int) {
	band.d[0] = d
	band.r[0] = g722Saturate(band.s + d)
	band.p[0] = g722Saturate(band.sz + d)

	for i := 0; i < 3; i++ {
		band.sg[i] = band.p[i] >> 15
	}

	wd1 := g722Saturate(band.a[1] << 2)

	wd2 := wd1
	if band.sg[0] == band.sg[1] {
		wd2 = -wd1
	}

	if wd2 > 32767 {
		wd2 = 32767
	}

	wd3 := wd2 >> 7
	if band.sg[0] == band.sg[2] {
		wd3 += 128
	} else {
		wd3 -= 128
	}

	wd3 += (band.a[2] * 32512) >> 15

	if wd3 > 12288 {
		wd3 = 12288
	} else if wd3 < -12288 {
		wd3 = -12288
	}

	band.ap[2] = wd3

	band.sg[0] = band.p[0] >> 15
	band.sg[1] = band.p[1] >> 15

	wd1 = -192
	if band.sg[0] == band.sg[1] {
		wd1 = 192
	}

	wd2 = (band.a[1] * 32640) >> 15

	band.ap[1] = g722Saturate(wd1 + wd2)

	wd3 = g722Saturate(15360 - band.ap[2])
	if band.ap[1] > wd3 {
		band.ap[1] = wd3
	} else if band.ap[1] < -wd3 {
		band.ap[1] = -wd3
	}

	wd1 = 128
	if d == 0 {
		wd1 = 0
	}

	band.sg[0] = d >> 15

	for i := 1; i < 7; i++ {
		band.sg[i] = band.d[i] >> 15

		wd2 = -wd1
		if band.sg[i] == band.sg[0] {
			wd2 = wd1
		}

		wd3 = (band.b[i] * 32640) >> 15
		band.bp[i] = g722Saturate(wd2 + wd3)
	}

	for i := 6; i > 0; i-- {
		band.d[i] = band.d[i-1]
		band.b[i] = band.bp[i]
	}

	for i := 2; i > 0; i-- {
		band.r[i] = band.r[i-1]
		band.p[i] = band.p[i-1]
		band.a[i] = band.ap[i]
	}

	wd1 = g722Saturate(band.r[1] + band.r[1])
	wd1 = (band.a[1] * wd1) >> 15
	wd2 = g722Saturate(band.r[2] + band.r[2])
	wd2 = (band.a[2] * wd2) >> 15
	band.sp = g722Saturate(wd1 + wd2)

	band.sz = 0

	for i := 6; i > 0; i-- {
		wd1 = g722Saturate(band.d[i] + band.d[i])
		band.sz += (band.b[i] * wd1) >> 15
	}

	band.sz = g722Saturate(band.sz)
	band.s = g722Saturate(band.sp + band.sz)
}

// scale обновляет логарифмический масштаб nb и шаг квантования det (блоки LOGSCL и SCALE)
func g722Scale(nb, weight, nbMax, shift int) (newNB, det int) {
	nb = ((nb * 127) >> 7) + weight

	if nb < 0 {
		nb = 0
	} else if nb > nbMax {
		nb = nbMax
	}

	wd1 := (nb >> 6) & 31
	wd2 := shift - (nb >> 11)

	var wd3 int
	if wd2 < 0 {
		wd3 = g722ILB[wd1] << uint(-wd2)
	} else {
		wd3 = g722ILB[wd1] >> uint(wd2)
	}

	return nb, wd3 << 2
}

// g722Encoder кодирует 16-битные отсчёты 16 кГц в байты G.722; состояние хранится между вызовами
type g722Encoder struct {
	x    [24]int
	band [2]g722Band
}

func newG722Encoder() *g722Encoder {
	return &g722Encoder{band: newG722Bands()}
}

// Encode кодирует чётное число отсчётов: на каждую пару — один байт. Нечётный последний отсчёт отбрасывается.
func (e *g722Encoder) Encode(pcm []int16, dst []byte) []byte {
	dst = dst[:0]

	for j := 0; j+1 < len(pcm); j += 2 {
		copy(e.x[:22], e.x[2:])
		e.x[22] = int(pcm[j])
		e.x[23] = int(pcm[j+1])

		sumEven, sumOdd := 0, 0

		for i := 0; i < 12; i++ {
			sumOdd += e.x[2*i] * g722QMFCoeffs[i]
			sumEven += e.x[2*i+1] * g722QMFCoeffs[11-i]
		}

		xlow := (sumEven + sumOdd) >> 14
		xhigh := (sumEven - sumOdd) >> 14

		dst = append(dst, byte(e.encodeHigh(xhigh)<<6|e.encodeLow(xlow)))
	}

	return dst
}

func (e *g722Encoder) encodeLow(xlow int) int {
	band := &e.band[0]

	el := g722Saturate(xlow - band.s)

	wd := el
	if el < 0 {
		wd = -(el + 1)
	}

	i := 1
	for ; i < 30; i++ {
		if wd < (g722Q6[i]*band.det)>>12 {
			break
		}
	}

	var ilow int
	if el < 0 {
		ilow = g722ILN[i]
	} else {
		ilow = g722ILP[i]
	}

	ril := ilow >> 2
	dlow := (band.det * g722QM4[ril]) >> 15

	band.nb, band.det = g722Scale(band.nb, g722WL[g722RL42[ril]], 18432, 8)
	band.adapt(dlow)

	return ilow
}

func (e *g722Encoder) encodeHigh(xhigh int) int {
	band := &e.band[1]

	eh := g722Saturate(xhigh - band.s)

	wd := eh
	if eh < 0 {
		wd = -(eh + 1)
	}

	mih := 1
	if wd >= (564*band.det)>>12 {
		mih = 2
	}

	var ihigh int
	if eh < 0 {
		ihigh = g722IHN[mih]
	} else {
		ihigh = g722IHP[mih]
	}

	dhigh := (band.det * g722QM2[ihigh]) >> 15

	band.nb, band.det = g722Scale(band.nb, g722WH[g722RH2[ihigh]], 22528, 10)
	band.adapt(dhigh)

	return ihigh
}

// g722Decoder декодирует байты G.722 в отсчёты 16 кГц
type g722Decoder struct {
	x    [24]int
	band [2]g722Band
}

func newG722Decoder() *g722Decoder {
	return &g722Decoder{band: newG722Bands()}
}

// Decode декодирует каждый байт в два отсчёта
func (d *g722Decoder) Decode(payload []byte, dst []int16) []int16 {
	dst = dst[:0]

	for _, code := range payload {

		wd1 := int(code & 0x3F)
		ihigh := int(code>>6) & 0x03

		band := &d.band[0]

		wd2 := (band.det * g722QM6[wd1]) >> 15
		rlow := clampG722(band.s + wd2)

		ril := wd1 >> 2
		dlowt := (band.det * g722QM4[ril]) >> 15

		band.nb, band.det = g722Scale(band.nb, g722WL[g722RL42[ril]], 18432, 8)
		band.adapt(dlowt)

		band = &d.band[1]

		dhigh := (band.det * g722QM2[ihigh]) >> 15
		rhigh := clampG722(band.s + dhigh)

		band.nb, band.det = g722Scale(band.nb, g722WH[g722RH2[ihigh]], 22528, 10)
		band.adapt(dhigh)

		copy(d.x[:22], d.x[2:])
		d.x[22] = rlow + rhigh
		d.x[23] = rlow - rhigh

		xout1, xout2 := 0, 0

		for i := 0; i < 12; i++ {
			xout2 += d.x[2*i] * g722QMFCoeffs[i]
			xout1 += d.x[2*i+1] * g722QMFCoeffs[11-i]
		}

		dst = append(dst, int16(g722Saturate(xout1>>11)), int16(g722Saturate(xout2>>11)))
	}

	return dst
}

func clampG722(x int) int {
	if x > 16383 {
		return 16383
	}

	if x < -16384 {
		return -16384
	}

	return x
}
