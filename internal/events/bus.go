// Package events — буфер событий, которые media-manager присылает тестеру.
//
// MM вызывает на тестере методы (AudioFilePlayed, AudioSequencePlayed и другие). Сервер тестера кладёт каждый
// такой вызов в Bus, а команда сценария wait_event ждёт подходящее событие. События буферизуются, поэтому
// событие, пришедшее до wait_event, не теряется.
package events

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxBuffered — сколько событий хранить; при переполнении самые старые выбрасываются
const maxBuffered = 10000

// maxShown — сколько неподошедших событий показывать в сообщении о таймауте
const maxShown = 5

type item struct {
	name     string
	json     string
	consumed bool
}

// Bus хранит полученные события и будит ожидающих при появлении новых
type Bus struct {
	mu      sync.Mutex
	items   []item
	changed chan struct{} // закрывается при каждой публикации и заменяется новым
}

func NewBus() *Bus {
	return &Bus{changed: make(chan struct{})}
}

// Publish добавляет событие: name — имя метода (например AudioFilePlayed), eventJSON — его параметры
func (bus *Bus) Publish(name, eventJSON string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.items = append(bus.items, item{name: name, json: eventJSON})
	if len(bus.items) > maxBuffered {
		bus.items = bus.items[len(bus.items)-maxBuffered:]
	}

	close(bus.changed)
	bus.changed = make(chan struct{})
}

// Reset забывает все накопленные события; вызывается перед каждым сценарием, чтобы события
// предыдущих сценариев не принимались за свои
func (bus *Bus) Reset() {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.items = nil
}

// Wait ждёт событие с именем name, для которого match вернул true, не дольше timeout.
// Берётся самое раннее подходящее событие из ещё не использованных, и оно помечается использованным:
// два вызова Wait подряд получат два разных события. match вызывается под блокировкой и не должен
// обращаться к Bus. Ошибка match прерывает ожидание. По истечении времени возвращается *TimeoutError.
func (bus *Bus) Wait(name string, match func(eventJSON string) (bool, error), timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for {
		bus.mu.Lock()

		for i := range bus.items {
			candidate := &bus.items[i]
			if candidate.consumed || candidate.name != name {
				continue
			}

			ok, err := match(candidate.json)
			if err != nil {
				bus.mu.Unlock()
				return "", err
			}

			if ok {
				candidate.consumed = true
				eventJSON := candidate.json
				bus.mu.Unlock()

				return eventJSON, nil
			}
		}

		changed := bus.changed

		remaining := time.Until(deadline)
		if remaining <= 0 {
			err := bus.timeoutError(name, timeout)
			bus.mu.Unlock()

			return "", err
		}

		bus.mu.Unlock()

		timer := time.NewTimer(remaining)

		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// timeoutError собирает диагностику: какие события этого имени приходили и не подошли, какие приходили другие
func (bus *Bus) timeoutError(name string, timeout time.Duration) *TimeoutError {
	result := &TimeoutError{Name: name, After: timeout, Others: map[string]int{}}

	for _, candidate := range bus.items {
		if candidate.consumed {
			continue
		}

		if candidate.name == name {
			result.Same = append(result.Same, candidate.json)
		} else {
			result.Others[candidate.name]++
		}
	}

	return result
}

// TimeoutError — событие не пришло за отведённое время
type TimeoutError struct {
	Name   string
	After  time.Duration
	Same   []string       // события с этим именем, не подошедшие под условия
	Others map[string]int // другие события: имя → сколько раз пришло
}

func (err *TimeoutError) Error() string {
	var text strings.Builder

	fmt.Fprintf(&text, "за %v не пришло событие %s", err.After, err.Name)

	if len(err.Same) == 0 {
		text.WriteString(" (событий с таким именем не было)")
	} else {
		shown := err.Same
		if len(shown) > maxShown {
			shown = shown[len(shown)-maxShown:]
		}

		fmt.Fprintf(&text, " (не подошли под условия %d событий с таким именем: %s)", len(err.Same), strings.Join(shown, ", "))
	}

	if len(err.Others) > 0 {
		names := make([]string, 0, len(err.Others))
		for other := range err.Others {
			names = append(names, other)
		}

		sort.Strings(names)

		parts := make([]string, 0, len(names))
		for _, other := range names {
			parts = append(parts, fmt.Sprintf("%s ×%d", other, err.Others[other]))
		}

		fmt.Fprintf(&text, "; другие события за это время: %s", strings.Join(parts, ", "))
	}

	return text.String()
}
