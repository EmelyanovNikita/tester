package events

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func always(string) (bool, error) { return true, nil }

func contains(substr string) func(string) (bool, error) {
	return func(eventJSON string) (bool, error) { return strings.Contains(eventJSON, substr), nil }
}

func TestWaitGetsBufferedEvent(t *testing.T) {
	bus := NewBus()
	bus.Publish("AudioFilePlayed", `{"playerId":1}`)

	got, err := bus.Wait("AudioFilePlayed", always, time.Second)
	if err != nil || got != `{"playerId":1}` {
		t.Fatalf("Wait: %q, %v", got, err)
	}
}

func TestWaitBlocksUntilPublish(t *testing.T) {
	bus := NewBus()

	done := make(chan string, 1)

	go func() {
		got, _ := bus.Wait("Ev", always, 5*time.Second)
		done <- got
	}()

	select {
	case <-done:
		t.Fatal("Wait вернулся до публикации события")
	case <-time.After(50 * time.Millisecond):
	}

	bus.Publish("Ev", `{"a":1}`)

	select {
	case got := <-done:
		if got != `{"a":1}` {
			t.Errorf("получено %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait не вернулся после публикации")
	}
}

func TestWaitSkipsNonMatching(t *testing.T) {
	bus := NewBus()
	bus.Publish("Ev", `{"channel":"1"}`)
	bus.Publish("Other", `{"channel":"2","x":1}`)
	bus.Publish("Ev", `{"channel":"2"}`)

	got, err := bus.Wait("Ev", contains(`"channel":"2"`), time.Second)
	if err != nil || got != `{"channel":"2"}` {
		t.Fatalf("Wait: %q, %v", got, err)
	}

	// не подошедшее событие осталось и может быть получено позже
	got, err = bus.Wait("Ev", contains(`"channel":"1"`), time.Second)
	if err != nil || got != `{"channel":"1"}` {
		t.Fatalf("Wait остатка: %q, %v", got, err)
	}
}

func TestEventConsumedOnce(t *testing.T) {
	bus := NewBus()
	bus.Publish("Ev", `{"n":1}`)

	if _, err := bus.Wait("Ev", always, time.Second); err != nil {
		t.Fatalf("первый Wait: %v", err)
	}

	_, err := bus.Wait("Ev", always, 30*time.Millisecond)

	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("второй Wait должен закончиться таймаутом: событие уже использовано, получили: %v", err)
	}
}

func TestEarliestEventFirst(t *testing.T) {
	bus := NewBus()
	bus.Publish("Ev", `{"n":1}`)
	bus.Publish("Ev", `{"n":2}`)

	first, _ := bus.Wait("Ev", always, time.Second)
	second, _ := bus.Wait("Ev", always, time.Second)

	if first != `{"n":1}` || second != `{"n":2}` {
		t.Errorf("порядок событий: %q, %q", first, second)
	}
}

func TestResetDropsEvents(t *testing.T) {
	bus := NewBus()
	bus.Publish("Ev", `{}`)
	bus.Reset()

	if _, err := bus.Wait("Ev", always, 30*time.Millisecond); err == nil {
		t.Fatal("после Reset старые события не должны приниматься")
	}
}

func TestMatchErrorStopsWait(t *testing.T) {
	bus := NewBus()
	bus.Publish("Ev", `{}`)

	boom := errors.New("сломался фильтр")

	_, err := bus.Wait("Ev", func(string) (bool, error) { return false, boom }, time.Second)
	if !errors.Is(err, boom) {
		t.Fatalf("ожидалась ошибка фильтра, получили: %v", err)
	}
}

func TestZeroTimeoutChecksBufferOnly(t *testing.T) {
	bus := NewBus()

	start := time.Now()

	if _, err := bus.Wait("Ev", always, 0); err == nil {
		t.Fatal("ожидался таймаут: событий нет")
	}

	if time.Since(start) > 500*time.Millisecond {
		t.Error("нулевой таймаут не должен ждать")
	}

	bus.Publish("Ev", `{}`)

	if _, err := bus.Wait("Ev", always, 0); err != nil {
		t.Errorf("событие уже в буфере, нулевой таймаут должен его вернуть: %v", err)
	}
}

func TestTimeoutErrorDiagnostics(t *testing.T) {
	bus := NewBus()
	bus.Publish("AudioSequencePlayed", `{"channelId":"7"}`)
	bus.Publish("AudioFilePlayed", `{"channelId":"9"}`)
	bus.Publish("AudioFilePlayed", `{"channelId":"9"}`)

	_, err := bus.Wait("AudioSequencePlayed", contains(`"channelId":"5"`), 20*time.Millisecond)
	if err == nil {
		t.Fatal("ожидался таймаут")
	}

	for _, want := range []string{
		"не пришло событие AudioSequencePlayed",
		`не подошли под условия 1 событий с таким именем: {"channelId":"7"}`,
		"AudioFilePlayed ×2",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q: %v", want, err)
		}
	}

	_, err = bus.Wait("Missing", always, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "событий с таким именем не было") {
		t.Errorf("сообщение для отсутствующего события: %v", err)
	}
}

func TestBufferIsBounded(t *testing.T) {
	bus := NewBus()

	for i := 0; i < maxBuffered+50; i++ {
		bus.Publish("Ev", `{}`)
	}

	bus.mu.Lock()
	n := len(bus.items)
	bus.mu.Unlock()

	if n != maxBuffered {
		t.Errorf("в буфере %d событий, ожидалось не больше %d", n, maxBuffered)
	}
}

func TestConcurrentPublishAndWait(t *testing.T) {
	bus := NewBus()

	const count = 50

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		received = map[string]int{}
	)

	for i := 0; i < count; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			got, err := bus.Wait("Ev", always, 5*time.Second)
			if err != nil {
				t.Errorf("Wait: %v", err)
				return
			}

			mu.Lock()
			received[got]++
			mu.Unlock()
		}()
	}

	for i := 0; i < count; i++ {
		go bus.Publish("Ev", strings.Repeat("x", i+1))
	}

	wg.Wait()

	// каждое событие получено ровно одним ожидающим
	if len(received) != count {
		t.Errorf("получено %d разных событий, ожидалось %d: %v", len(received), count, received)
	}

	for event, n := range received {
		if n != 1 {
			t.Errorf("событие %q получено %d раз", event, n)
		}
	}
}
