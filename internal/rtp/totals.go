package rtp

// silentGrace (секунды; переменная, чтобы тесты могли сократить) — сколько секунд поток должен идти, прежде чем отсутствие входящих пакетов считается проблемой:
// MM не сразу начинает слать звук после ответа на SDP.
var silentGrace = 2.0

// Totals — суммарные счётчики всех RTP-сеансов фабрики с момента запуска тестера: и идущих сейчас, и уже завершённых.
// Нужны, чтобы во время теста видеть, шлёт ли MM трафик на тестер (экран нагрузки), не дожидаясь конца звонков.
type Totals struct {
	SessionsRunning  int // потоки, которые идут сейчас
	SessionsSilent   int // из идущих: дольше silentGrace секунд, а входящих пакетов нет вообще
	SessionsFinished int // потоки, которые уже остановлены
	FinishedSilent   int // из остановленных: работали дольше silentGrace секунд и не получили ни одного пакета

	SentPackets uint64 // отправлено тестером

	// Входящее от MM. IncomingPackets — все датаграммы, дошедшие до порта: звук, события, неизвестный payload type
	// и не-RTP. Остальные поля — его части (кроме LostPackets: это пакеты, которых не хватило в нумерации).
	IncomingPackets uint64
	AudioPackets    uint64
	DTMFPackets     uint64
	OtherPackets    uint64
	BadPackets      uint64
	LostPackets     uint64

	MaxJitterMs float64 // наибольший джиттер среди потоков
}

// incoming — все датаграммы, принятые потоком
func incoming(st Stats) uint64 {
	return st.ReceivedPackets + st.DTMFPackets + st.OtherPayloadPackets + st.BadPackets
}

// add прибавляет счётчики одного потока
func (t *Totals) add(st Stats) {
	t.SentPackets += st.SentPackets
	t.IncomingPackets += incoming(st)
	t.AudioPackets += st.ReceivedPackets
	t.DTMFPackets += st.DTMFPackets
	t.OtherPackets += st.OtherPayloadPackets
	t.BadPackets += st.BadPackets
	t.LostPackets += st.LostPackets

	if st.JitterMs > t.MaxJitterMs {
		t.MaxJitterMs = st.JitterMs
	}
}

// silent сообщает, что поток идёт достаточно давно, а входящих пакетов нет
func silent(st Stats) bool {
	return st.DurationSec >= silentGrace && incoming(st) == 0
}

// track запоминает идущий поток; вызывается из Open
func (f *Factory) track(s *Session) {
	f.statsMu.Lock()
	defer f.statsMu.Unlock()

	if f.live == nil {
		f.live = make(map[*Session]struct{})
	}

	f.live[s] = struct{}{}
}

// untrack переносит итог остановленного потока в накопленные счётчики; вызывается из Stop
func (f *Factory) untrack(s *Session, final Stats) {
	f.statsMu.Lock()
	defer f.statsMu.Unlock()

	if _, ok := f.live[s]; !ok {
		return
	}

	delete(f.live, s)

	f.closed.SessionsFinished++
	f.closed.add(final)

	if silent(final) {
		f.closed.FinishedSilent++
	}
}

// Totals возвращает суммарные счётчики по всем потокам фабрики. Безопасно вызывать во время работы потоков.
func (f *Factory) Totals() Totals {
	f.statsMu.Lock()
	totals := f.closed
	live := make([]*Session, 0, len(f.live))

	for s := range f.live {
		live = append(live, s)
	}
	f.statsMu.Unlock()

	// потоки опрашиваются без блокировки фабрики: у каждого своя блокировка
	for _, s := range live {
		if !s.Running() {
			continue
		}

		st := s.Stats()
		totals.SessionsRunning++
		totals.add(st)

		if silent(st) {
			totals.SessionsSilent++
		}
	}

	return totals
}
