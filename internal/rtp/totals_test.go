package rtp

import (
	"testing"
	"time"
)

func startQuiet(t *testing.T, f *Factory) *Session {
	t.Helper()

	s := openSession(t, f, OpenOptions{})

	if err := s.Start(StartOptions{RemoteSDP: answerSDP(4000, "0"), NoSend: true}); err != nil {
		t.Fatal(err)
	}

	return s
}

// Totals суммирует идущие потоки и находит те, на которые MM не шлёт ничего
func TestTotalsRunningAndSilent(t *testing.T) {
	old := silentGrace
	silentGrace = 0.05

	defer func() { silentGrace = old }()

	f := newTestFactory(t)
	loud := startQuiet(t, f)
	quiet := startQuiet(t, f)
	_ = quiet

	now := time.Now()

	for i := 0; i < 5; i++ {
		loud.handlePacket(packetAt(1, uint16(i), uint32(i)*160, 0, pcmuSilence), now)
	}

	loud.handlePacket([]byte{1, 2, 3}, now) // мусор тоже считается входящим

	time.Sleep(100 * time.Millisecond) // дольше silentGrace

	got := f.Totals()
	if got.SessionsRunning != 2 || got.SessionsSilent != 1 {
		t.Errorf("идёт %d, из них без входящих %d; ожидалось 2 и 1", got.SessionsRunning, got.SessionsSilent)
	}

	if got.IncomingPackets != 6 || got.AudioPackets != 5 || got.BadPackets != 1 {
		t.Errorf("входящих %d (звук %d, мусор %d), ожидалось 6 (5, 1)", got.IncomingPackets, got.AudioPackets, got.BadPackets)
	}
}

// После остановки потока его счётчики остаются в итогах, а не пропадают
func TestTotalsKeepsFinishedSessions(t *testing.T) {
	old := silentGrace
	silentGrace = 0.05

	defer func() { silentGrace = old }()

	f := newTestFactory(t)
	loud := startQuiet(t, f)
	quiet := startQuiet(t, f)

	for i := 0; i < 3; i++ {
		loud.handlePacket(packetAt(1, uint16(i), uint32(i)*160, 0, pcmuSilence), time.Now())
	}

	time.Sleep(100 * time.Millisecond)

	loud.Stop()
	quiet.Stop()

	got := f.Totals()
	if got.SessionsRunning != 0 || got.SessionsFinished != 2 || got.FinishedSilent != 1 || got.IncomingPackets != 3 {
		t.Errorf("после остановки: %+v", got)
	}

	// повторная остановка не удваивает итоги
	loud.Stop()

	if again := f.Totals(); again.SessionsFinished != 2 || again.IncomingPackets != 3 {
		t.Errorf("повторный Stop изменил итоги: %+v", again)
	}
}

// Поток, который открыт, но не запущен, в идущих не учитывается и «тишиной» не считается
func TestTotalsIgnoresNotStarted(t *testing.T) {
	f := newTestFactory(t)
	openSession(t, f, OpenOptions{})

	if got := f.Totals(); got.SessionsRunning != 0 || got.SessionsSilent != 0 {
		t.Errorf("%+v", got)
	}
}
