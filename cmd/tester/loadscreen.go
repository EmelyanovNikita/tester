package main

import (
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/load"
	"tester_mm/internal/screen"
)

// screenLogFile — файл, куда в режиме --screen пишется лог тестера: на экране он мешал бы таблице
const screenLogFile = "tester.log"

// stopper — «стоп-кран» нагрузки: после Stop новые звонки не запускаются, идущие доигрываются.
// Срабатывает от первого Ctrl+C и от клавиши q на экране.
type stopper struct {
	ch   chan struct{}
	once sync.Once
}

func newStopper() *stopper {
	s := &stopper{ch: make(chan struct{})}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signals
		log.Print("Ctrl+C: новые звонки не запускаются, ждём завершения идущих (ещё раз Ctrl+C — выход сразу)")
		s.Stop()
		<-signals
		exitNow()
	}()

	return s
}

// Stop прекращает запуск новых звонков; можно вызывать повторно
func (s *stopper) Stop() {
	s.once.Do(func() { close(s.ch) })
}

// C закрывается после Stop
func (s *stopper) C() <-chan struct{} {
	return s.ch
}

// Stopped сообщает, что Stop уже вызван
func (s *stopper) Stopped() bool {
	select {
	case <-s.ch:
		return true
	default:
		return false
	}
}

// restoreTerminal возвращает терминал в обычный режим перед немедленным выходом; задаётся при запуске экрана
var restoreTerminal = func() {}

// exitNow завершает тестер сразу (второй Ctrl+C, клавиша Q), не ломая терминал
func exitNow() {
	restoreTerminal()
	os.Exit(130)
}

// startScreenMode включает режим экрана: лог переключается в файл, терминал — в посимвольный ввод.
// Возвращённая функция возвращает всё обратно (лог снова в stderr, терминал в обычный режим).
func startScreenMode(stop *stopper) (*screen.Session, func(), error) {
	session, err := screen.Start()
	if err != nil {
		return nil, nil, err
	}

	file, err := os.OpenFile(screenLogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		session.Close()

		return nil, nil, err
	}

	log.SetOutput(file)

	restoreTerminal = session.Close

	end := func() {
		session.Close()
		log.SetOutput(os.Stderr)
		_ = file.Close()
	}

	return session, end, nil
}

// showScreen запускает перерисовку экрана в отдельной горутине. Возвращённая функция останавливает её
// после последнего кадра и ждёт, пока кадр будет выведен.
func showScreen(session *screen.Session, opts load.Options, collector *cmdstats.Collector, scenarioPath string, stop *stopper) func() {
	base := screen.View{
		Scenario: scenarioPath,
		Calls:    opts.Calls,
		Parallel: opts.Parallel,
		LogFile:  screenLogFile,
	}

	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)

		session.Show(opts.Control, collector, base, done, func(action screen.Action) {
			switch action {
			case screen.ActionStop:
				stop.Stop()
			case screen.ActionQuit:
				exitNow()
			case screen.ActionNone:
			}
		})
	}()

	return func() {
		close(done)
		<-finished
	}
}
