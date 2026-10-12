package scenario

import (
	"errors"
	"strings"
	"testing"
	"time"

	"tester_mm/internal/events"
	"tester_mm/internal/protoloader"
)

// ===== Разбор =====

func TestParseWaitEvent(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
wait_event(Echo)
ev = wait_event("Echo", channelId == resp.channelId, info.type != 2, 5)
wait_event ( Echo , t )
`)

	if len(s.Commands) != 3 {
		t.Fatalf("команд: %d, ожидалось 3", len(s.Commands))
	}

	plain := s.Commands[0]
	if !plain.IsWaitEvent || plain.EventName != "Echo" || len(plain.EventConds) != 0 || plain.EventTimeout != "" || plain.VarName != "" {
		t.Errorf("wait_event(Echo) разобран неверно: %+v", plain)
	}

	full := s.Commands[1]
	if !full.IsWaitEvent || full.VarName != "ev" || full.EventName != "Echo" || full.EventTimeout != "5" || len(full.EventConds) != 2 {
		t.Fatalf("полная форма разобрана неверно: %+v", full)
	}

	if c := full.EventConds[0]; c.Field != "channelId" || c.Op != "==" || c.Value != "resp.channelId" {
		t.Errorf("первое условие: %+v", c)
	}

	if c := full.EventConds[1]; c.Field != "info.type" || c.Op != "!=" || c.Value != "2" {
		t.Errorf("второе условие: %+v", c)
	}

	if s.Commands[2].EventTimeout != "t" {
		t.Errorf("таймаут-переменная разобрана неверно: %+v", s.Commands[2])
	}
}

func TestParseWaitEventErrors(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"без скобок", "wait_event Echo\n", "неверный синтаксис wait_event"},
		{"без аргументов", "wait_event()\n", "неверный синтаксис wait_event"},
		{"неизвестное событие", "wait_event(Nope)\n", "событие 'Nope' не найдено"},
		{"неверное имя", "wait_event(1abc)\n", "неверное имя события"},
		{"слева не поле", "wait_event(Echo, 1 == x)\n", "должно быть имя поля события"},
		{"два таймаута", "wait_event(Echo, 5, 6)\n", "таймаут уже задан"},
		{"пустой аргумент", "wait_event(Echo, , 5)\n", "пустой аргумент"},
		{"одиночное =", "wait_event(Echo, a = 1)\n", "неизвестный оператор '='"},
		{"&&", "wait_event(Echo, a == 1 && b == 2)\n", "не поддерживается"},
		{"присваивание с ошибкой", "ev = wait_event(Nope)\n", "событие 'Nope' не найдено"},
	}

	for _, c := range cases {
		_, err := NewParser(data.Methods).Parse(c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "строка 1") {
			t.Errorf("%s: ожидалась ошибка со «строка 1» и %q, получили: %v", c.name, c.want, err)
		}
	}
}

func TestParseWaitEventNameAsVariable(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "wait_event = 1\nwait_event_count = 2\n")

	for i, cmd := range s.Commands {
		if cmd.IsWaitEvent || !cmd.IsSimpleAssign {
			t.Errorf("команда %d принята за wait_event: %+v", i+1, cmd)
		}
	}
}

// ===== Выполнение =====

func newEventExecutor(t *testing.T) (*Executor, *events.Bus, *protoloader.ProtoData) {
	t.Helper()

	executor, data := newBareExecutor(t)
	bus := events.NewBus()
	executor.SetEvents(bus)

	return executor, bus, data
}

func TestExecuteWaitEvent(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	executor.exprParser.vars.Set("resp", `{"channelId":"72057594037928107"}`)

	bus.Publish("Echo", `{"channelId":"72057594037928106","playerId":1}`) // чужой канал
	bus.Publish("Echo", `{"channelId":"72057594037928107","playerId":2,"replyCode":"RC_FAIL"}`)

	s := mustParse(t, data, `
ev = wait_event(Echo, channelId == resp.channelId, playerId >= 2, replyCode == RC_FAIL, 0)
id = ev.channelId
player = ev.playerId
`)

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "id"); got != "72057594037928107" {
		t.Errorf("id = %q", got)
	}

	if got, _ := varValue(executor, "player"); got != "2" {
		t.Errorf("player = %q", got)
	}
}

func TestExecuteWaitEventWithoutVariable(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	bus.Publish("Echo", `{"playerId":1}`)

	if err := executor.Execute(mustParse(t, data, "wait_event(Echo, playerId == 1, 0)\n")); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if executor.executed != 1 {
		t.Errorf("выполнено команд: %d, ожидалось 1", executor.executed)
	}
}

// Id больше 2^53 сравниваются точно: соседние значения не путаются
func TestWaitEventExactIDs(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	executor.exprParser.vars.Set("resp", `{"channelId":"72057594037928108"}`)
	bus.Publish("Echo", `{"channelId":"72057594037928107"}`)

	err := executor.Execute(mustParse(t, data, "wait_event(Echo, channelId == resp.channelId, 0)\n"))
	if err == nil || !strings.Contains(err.Error(), "не пришло событие Echo") {
		t.Fatalf("соседний id не должен подходить, получили: %v", err)
	}
}

func TestWaitEventEachEventOnce(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	bus.Publish("Echo", `{"playerId":1}`)
	bus.Publish("Echo", `{"playerId":2}`)

	s := mustParse(t, data, "a = wait_event(Echo, 0)\nb = wait_event(Echo, 0)\n")
	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	a, _ := varValue(executor, "a")
	b, _ := varValue(executor, "b")

	if a != `{"playerId":1}` || b != `{"playerId":2}` {
		t.Errorf("a=%s b=%s: два ожидания должны получить два разных события по порядку", a, b)
	}

	// третьего события нет
	err := executor.Execute(mustParse(t, data, "wait_event(Echo, 0)\n"))
	if err == nil {
		t.Error("событие уже использовано, ожидался таймаут")
	}
}

func TestWaitEventInLoopWithIf(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	for i := 1; i <= 3; i++ {
		bus.Publish("Echo", `{"playerId":`+string(rune('0'+i))+`}`)
	}

	s := mustParse(t, data, `
last = 0
for i in 1..3
{
    ev = wait_event(Echo, playerId == i, 0)
    if ev.playerId == 3
    {
        last = i
    }
}
`)

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "last"); got != "3" {
		t.Errorf("last = %q, ожидалось \"3\"", got)
	}
}

func TestWaitEventMissingFieldDoesNotMatch(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	bus.Publish("Echo", `{"playerId":1}`)

	err := executor.Execute(mustParse(t, data, "wait_event(Echo, nope == 1, 0)\n"))
	if err == nil || !strings.Contains(err.Error(), `не подошли под условия 1 событий`) || !strings.Contains(err.Error(), `{"playerId":1}`) {
		t.Fatalf("ожидалась ошибка с показом неподошедшего события, получили: %v", err)
	}
}

func TestWaitEventErrors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"неизвестная переменная в условии", "wait_event(Echo, playerId == nope, 0)\n", "wait_event: условие"},
		{"порядок у строк", "wait_event(Echo, name < \"b\", 0)\n", "применим только к числам"},
		{"отрицательный таймаут", "wait_event(Echo, -1)\n", "таймаут"},
		{"таймаут не число", "wait_event(Echo, abc)\n", "таймаут"},
		{"таймаут слишком большой", "wait_event(Echo, 100000)\n", "таймаут"},
	}

	for _, c := range cases {
		executor, bus, data := newEventExecutor(t)
		bus.Publish("Echo", `{"playerId":1,"name":"a"}`)

		err := executor.Execute(mustParse(t, data, c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "команде 1") {
			t.Errorf("%s: ожидалась ошибка с %q и номером команды, получили: %v", c.name, c.want, err)
		}
	}
}

func TestWaitEventNotConfigured(t *testing.T) {
	executor, data := newBareExecutor(t)

	err := executor.Execute(mustParse(t, data, "wait_event(Echo)\n"))
	if err == nil || !strings.Contains(err.Error(), "приём событий не настроен") {
		t.Fatalf("ожидалась ошибка «не настроен», получили: %v", err)
	}
}

// fakeEvents запоминает, с каким таймаутом вызвали Wait
type fakeEvents struct {
	timeout time.Duration
	calls   int
}

func (f *fakeEvents) Wait(_ string, _ func(string) (bool, error), timeout time.Duration) (string, error) {
	f.calls++
	f.timeout = timeout

	return "{}", nil
}

func TestWaitEventTimeouts(t *testing.T) {
	cases := []struct {
		name string
		text string
		want time.Duration
	}{
		{"по умолчанию 10 секунд", "wait_event(Echo)\n", 10 * time.Second},
		{"число", "wait_event(Echo, 2.5)\n", 2500 * time.Millisecond},
		{"переменная", "t = 3\nwait_event(Echo, t)\n", 3 * time.Second},
		{"ноль", "wait_event(Echo, 0)\n", 0},
		{"после условий", "wait_event(Echo, playerId == 1, 4)\n", 4 * time.Second},
	}

	for _, c := range cases {
		executor, data := newBareExecutor(t)

		fake := &fakeEvents{}
		executor.SetEvents(fake)

		if err := executor.Execute(mustParse(t, data, c.text)); err != nil {
			t.Errorf("%s: Execute: %v", c.name, err)
			continue
		}

		if fake.calls != 1 || fake.timeout != c.want {
			t.Errorf("%s: Wait вызван %d раз с таймаутом %v, ожидалось 1 раз с %v", c.name, fake.calls, fake.timeout, c.want)
		}
	}
}

func TestWaitEventSourceErrorPropagates(t *testing.T) {
	executor, data := newBareExecutor(t)

	executor.SetEvents(failingEvents{})

	err := executor.Execute(mustParse(t, data, "ev = wait_event(Echo)\nx = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "wait_event: источник сломан") || !strings.Contains(err.Error(), "команде 1") {
		t.Fatalf("ошибка источника: %v", err)
	}

	if _, ok := varValue(executor, "ev"); ok {
		t.Error("при ошибке переменная не должна создаваться")
	}
}

type failingEvents struct{}

func (failingEvents) Wait(string, func(string) (bool, error), time.Duration) (string, error) {
	return "", errors.New("источник сломан")
}

// Числа в JSON ответа и событий выводятся в исходной записи, без экспоненты
func TestObjectPathKeepsNumbers(t *testing.T) {
	executor, _ := newBareExecutor(t)

	executor.exprParser.vars.Set("obj", `{"big":1000000,"half":0.5,"id":"72057594037927936","nested":{"n":12345678901}}`)

	for path, want := range map[string]string{
		"obj.big":      "1000000",
		"obj.half":     "0.5",
		"obj.id":       "72057594037927936",
		"obj.nested.n": "12345678901",
	} {
		got, err := executor.exprParser.ResolveValue(path)
		if err != nil || got != want {
			t.Errorf("%s = %q (ошибка %v), ожидалось %q", path, got, err, want)
		}
	}

	if _, err := executor.exprParser.ResolveValue("obj.nope"); err == nil || !strings.Contains(err.Error(), "obj.nope") {
		t.Errorf("ошибка для несуществующего поля должна называть путь: %v", err)
	}
}

// Условий может быть сколько угодно, все объединяются через И:
// достаточно одного невыполненного, чтобы событие не подошло.
func TestWaitEventManyConditionsAreAnded(t *testing.T) {
	const script = "ev = wait_event(Echo, channelId == 7, playerId == 2, info.type == 3, replyCode == RC_FAIL, 0)\n"

	cases := []struct {
		name  string
		event string
		match bool
	}{
		{"все выполнены", `{"channelId":"7","playerId":2,"info":{"type":3},"replyCode":"RC_FAIL"}`, true},
		{"не совпал channelId", `{"channelId":"8","playerId":2,"info":{"type":3},"replyCode":"RC_FAIL"}`, false},
		{"не совпал playerId", `{"channelId":"7","playerId":1,"info":{"type":3},"replyCode":"RC_FAIL"}`, false},
		{"не совпало вложенное поле", `{"channelId":"7","playerId":2,"info":{"type":4},"replyCode":"RC_FAIL"}`, false},
		{"не совпал enum", `{"channelId":"7","playerId":2,"info":{"type":3},"replyCode":"RC_SUCCESS"}`, false},
		{"нет вложенного объекта", `{"channelId":"7","playerId":2,"replyCode":"RC_FAIL"}`, false},
	}

	for _, c := range cases {
		executor, bus, data := newEventExecutor(t)
		bus.Publish("Echo", c.event)

		err := executor.Execute(mustParse(t, data, script))

		if c.match && err != nil {
			t.Errorf("%s: событие должно подойти, получили: %v", c.name, err)
		}

		if !c.match && (err == nil || !strings.Contains(err.Error(), "не пришло событие Echo")) {
			t.Errorf("%s: событие не должно подойти, получили: %v", c.name, err)
		}
	}
}

// Одно поле можно использовать в нескольких условиях: так задаётся диапазон
func TestWaitEventRangeOnSameField(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	bus.Publish("Echo", `{"duration":1}`)
	bus.Publish("Echo", `{"duration":50}`)
	bus.Publish("Echo", `{"duration":5}`)

	s := mustParse(t, data, "ev = wait_event(Echo, duration > 1, duration < 10, 0)\nd = ev.duration\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "d"); got != "5" {
		t.Errorf("d = %q, ожидалось 5 (единственное событие внутри диапазона)", got)
	}
}

// Без условий подходит любое событие с таким именем, берётся самое раннее
func TestWaitEventNoConditionsTakesEarliest(t *testing.T) {
	executor, bus, data := newEventExecutor(t)

	bus.Publish("Echo", `{"playerId":1}`)
	bus.Publish("Echo", `{"playerId":2}`)

	s := mustParse(t, data, "a = wait_event(Echo, 0)\nb = wait_event(Echo, 0)\nx = a.playerId\ny = b.playerId\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	x, _ := varValue(executor, "x")
	y, _ := varValue(executor, "y")

	if x != "1" || y != "2" {
		t.Errorf("x=%q y=%q, ожидалось 1 и 2", x, y)
	}
}
