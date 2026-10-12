package mmlink

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEstablisher считает вызовы и запоминает параметры; соединение не создаёт
type fakeEstablisher struct {
	calls  atomic.Int32
	failTo int32 // первые failTo вызовов завершаются ошибкой
	delay  time.Duration
	mu     sync.Mutex
	params []Params
}

func (f *fakeEstablisher) establish(_ context.Context, params Params) (*Session, error) {
	n := f.calls.Add(1)

	time.Sleep(f.delay)

	f.mu.Lock()
	f.params = append(f.params, params)
	f.mu.Unlock()

	if n <= f.failTo {
		return nil, errors.New("не удалось подключиться")
	}

	return &Session{ID: params.SessionID}, nil
}

func TestEnsureWaitsForConnect(t *testing.T) {
	fake := &fakeEstablisher{}
	link := New(fake.establish)

	if link.Session() != nil {
		t.Fatal("до Connect сессии быть не должно")
	}

	type result struct {
		session *Session
		err     error
	}

	done := make(chan result, 1)

	go func() {
		session, err := link.Ensure(context.Background())
		done <- result{session, err}
	}()

	select {
	case <-done:
		t.Fatal("Ensure вернулся до того, как MM прислал Connect")
	case <-time.After(50 * time.Millisecond):
	}

	if !link.OnConnect(Params{SessionID: "s1", Address: "127.0.0.1", Port: 7001}) {
		t.Fatal("первый Connect должен быть принят")
	}

	select {
	case res := <-done:
		if res.err != nil || res.session == nil || res.session.ID != "s1" {
			t.Fatalf("Ensure: session=%+v err=%v", res.session, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure не вернулся после Connect")
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	fake := &fakeEstablisher{}
	link := New(fake.establish)
	link.OnConnect(Params{SessionID: "s1"})

	first, err := link.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	second, err := link.Ensure(context.Background())
	if err != nil {
		t.Fatalf("повторный Ensure: %v", err)
	}

	if first != second {
		t.Error("повторный Ensure должен вернуть ту же сессию")
	}

	if got := fake.calls.Load(); got != 1 {
		t.Errorf("соединение устанавливалось %d раз, ожидалось 1", got)
	}

	if link.Session() != first {
		t.Error("Session() должен вернуть установленную сессию")
	}
}

func TestDuplicateConnectIgnored(t *testing.T) {
	fake := &fakeEstablisher{}
	link := New(fake.establish)

	if !link.OnConnect(Params{SessionID: "first"}) {
		t.Fatal("первый Connect должен быть принят")
	}

	if link.OnConnect(Params{SessionID: "second"}) {
		t.Fatal("повторный Connect должен быть проигнорирован")
	}

	session, err := link.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if session.ID != "first" {
		t.Errorf("использованы параметры %q, ожидались параметры первого Connect", session.ID)
	}
}

func TestEnsureCanceledBeforeConnect(t *testing.T) {
	link := New((&fakeEstablisher{}).establish)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := link.Ensure(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидалась ошибка DeadlineExceeded, получили: %v", err)
	}
}

func TestEnsureRetriesAfterFailure(t *testing.T) {
	fake := &fakeEstablisher{failTo: 1}
	link := New(fake.establish)
	link.OnConnect(Params{SessionID: "s1", Port: 7001})

	if _, err := link.Ensure(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка первой попытки")
	}

	if link.Session() != nil {
		t.Fatal("после ошибки сессии быть не должно")
	}

	session, err := link.Ensure(context.Background())
	if err != nil || session.ID != "s1" {
		t.Fatalf("вторая попытка: session=%+v err=%v", session, err)
	}

	// обе попытки использовали одни и те же параметры из Connect
	if len(fake.params) != 2 || fake.params[0] != fake.params[1] {
		t.Errorf("параметры попыток: %+v", fake.params)
	}
}

func TestEnsureConcurrent(t *testing.T) {
	// медленное подключение: вызовы Ensure гарантированно пересекаются во времени
	fake := &fakeEstablisher{delay: 30 * time.Millisecond}
	link := New(fake.establish)
	link.OnConnect(Params{SessionID: "s1"})

	var wg sync.WaitGroup

	sessions := make([]*Session, 20)

	for i := range sessions {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			sessions[i], _ = link.Ensure(context.Background())
		}(i)
	}

	wg.Wait()

	if got := fake.calls.Load(); got != 1 {
		t.Errorf("соединение устанавливалось %d раз, ожидалось 1", got)
	}

	for i, session := range sessions {
		if session == nil || session != sessions[0] {
			t.Fatalf("горутина %d получила другую сессию: %+v", i, session)
		}
	}
}
