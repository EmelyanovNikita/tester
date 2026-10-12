package scenario

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// EventSource — откуда wait_event берёт события от MM (реализует events.Bus)
type EventSource interface {
	// Wait ждёт не использованное ранее событие с именем name, для которого match вернул true.
	// Возвращает JSON параметров события.
	Wait(name string, match func(eventJSON string) (bool, error), timeout time.Duration) (string, error)
}

// defaultEventTimeoutSeconds — сколько ждать событие, если таймаут в wait_event не указан
const defaultEventTimeoutSeconds = 10

// SetEvents задаёт источник событий для wait_event
func (executor *Executor) SetEvents(source EventSource) {
	executor.events = source
}

// runWaitEvent выполняет [ev =] wait_event(Имя, поле == значение, ..., секунды):
// ждёт событие, у которого все условия выполняются, и сохраняет его параметры (JSON) в переменную
func (executor *Executor) runWaitEvent(label string, cmd Command) error {
	if executor.events == nil {
		return fmt.Errorf("wait_event: приём событий не настроен")
	}

	timeout, err := executor.eventTimeout(cmd)
	if err != nil {
		return err
	}

	// Правые части условий вычисляются один раз, в момент начала ожидания
	rights := make([]string, len(cmd.EventConds))
	described := make([]string, len(cmd.EventConds))

	for i, cond := range cmd.EventConds {
		right, err := executor.condOperand(cond.Value)
		if err != nil {
			return fmt.Errorf("wait_event: условие %q: %w", cond.Field+" "+cond.Op+" "+cond.Value, err)
		}

		rights[i] = right
		described[i] = fmt.Sprintf("%s %s %s", cond.Field, cond.Op, right)
	}

	match := func(eventJSON string) (bool, error) {
		for i, cond := range cmd.EventConds {
			value, err := jsonFieldPath(eventJSON, strings.Split(cond.Field, "."))
			if err != nil {
				// у этого события нет такого поля — оно не подходит
				return false, nil
			}

			ok, err := compareValues(executor.enumToNumber(value), cond.Op, rights[i])
			if err != nil {
				return false, fmt.Errorf("условие %q: %w", described[i], err)
			}

			if !ok {
				return false, nil
			}
		}

		return true, nil
	}

	conditions := ""
	if len(described) > 0 {
		conditions = ", " + strings.Join(described, ", ")
	}

	log.Printf("[%s] wait_event %s%s, таймаут %v", label, cmd.EventName, conditions, timeout)

	eventJSON, err := executor.events.Wait(cmd.EventName, match, timeout)
	if err != nil {
		return fmt.Errorf("wait_event: %w", err)
	}

	log.Printf("[%s] получено событие %s: %s", label, cmd.EventName, eventJSON)

	if cmd.VarName != "" {
		executor.exprParser.vars.Set(cmd.VarName, eventJSON)
		log.Printf("[%s] событие сохранено в переменную '%s'", label, cmd.VarName)
	}

	return nil
}

// eventTimeout вычисляет таймаут wait_event: число секунд (может быть дробным и 0) или переменная
func (executor *Executor) eventTimeout(cmd Command) (time.Duration, error) {
	if cmd.EventTimeout == "" {
		return defaultEventTimeoutSeconds * time.Second, nil
	}

	seconds, err := executor.resolveSeconds(cmd.EventTimeout)
	if err != nil {
		return 0, fmt.Errorf("wait_event: таймаут: %w", err)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}
