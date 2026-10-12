package scenario

import (
	"strings"
	"testing"
	"time"

	"tester_mm/internal/protoloader"
)

func mustParse(t *testing.T, data *protoloader.ProtoData, text string) *Scenario {
	t.Helper()

	s, err := NewParser(data.Methods).Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	return s
}

// newBareExecutor создаёт исполнитель без соединения: подходит для сценариев из присваиваний, циклов и пауз
func newBareExecutor(t *testing.T) (*Executor, *protoloader.ProtoData) {
	t.Helper()

	data := loadTestProto(t)

	return NewExecutor(nil, data.Methods, "sess1", data.Enums), data
}

func varValue(executor *Executor, name string) (string, bool) {
	return executor.exprParser.vars.Get(name)
}

// ===== Разбор =====

func TestParseFor(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "for i in 1..3 {\n    x = i\n    y = 2\n}\nz = 1\n")

	if len(s.Commands) != 2 {
		t.Fatalf("команд верхнего уровня: %d, ожидалось 2", len(s.Commands))
	}

	loop := s.Commands[0]
	if !loop.IsFor || loop.LoopVar != "i" || loop.LoopFrom != "1" || loop.LoopTo != "3" {
		t.Fatalf("неверно разобран заголовок цикла: for=%v var=%q from=%q to=%q", loop.IsFor, loop.LoopVar, loop.LoopFrom, loop.LoopTo)
	}

	if len(loop.Body) != 2 {
		t.Fatalf("команд в теле цикла: %d, ожидалось 2", len(loop.Body))
	}

	if !s.Commands[1].IsSimpleAssign || s.Commands[1].VarName != "z" {
		t.Fatalf("команда после цикла разобрана неверно: %+v", s.Commands[1])
	}
}

func TestParseForHeaderVariants(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		header string
		from   string
		to     string
	}{
		{"for i in 1..10 {", "1", "10"},
		{"for i in 1 .. 10 {", "1", "10"},
		{"for i in 1..10{", "1", "10"},
		{"for idx in start..n {", "start", "n"},
		{"for i in resp.count..10 {", "resp.count", "10"},
	}

	for _, c := range cases {
		s := mustParse(t, data, c.header+"\n}\n")

		loop := s.Commands[0]
		if loop.LoopFrom != c.from || loop.LoopTo != c.to {
			t.Errorf("%q: границы %q..%q, ожидалось %q..%q", c.header, loop.LoopFrom, loop.LoopTo, c.from, c.to)
		}
	}
}

func TestParseNestedFor(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
for i in 1..2 {
    for j in 1..3 {
        n = j
    }
    m = i
}
`)

	outer := s.Commands[0]
	if len(outer.Body) != 2 || !outer.Body[0].IsFor || !outer.Body[1].IsSimpleAssign {
		t.Fatalf("неверная структура внешнего цикла: %+v", outer.Body)
	}

	inner := outer.Body[0]
	if inner.LoopVar != "j" || len(inner.Body) != 1 {
		t.Fatalf("неверный вложенный цикл: var=%q, команд в теле: %d", inner.LoopVar, len(inner.Body))
	}
}

func TestParseSleep(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "sleep(0.5)\nsleep (n)\n")

	if len(s.Commands) != 2 {
		t.Fatalf("команд: %d, ожидалось 2", len(s.Commands))
	}

	if !s.Commands[0].IsSleep || s.Commands[0].SleepArg != "0.5" {
		t.Errorf("sleep(0.5) разобран неверно: %+v", s.Commands[0])
	}

	if !s.Commands[1].IsSleep || s.Commands[1].SleepArg != "n" {
		t.Errorf("sleep (n) разобран неверно: %+v", s.Commands[1])
	}
}

func TestParseErrors(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"цикл не закрыт", "for i in 1..3 {\n    x = 1\n", "строка 1: цикл for не закрыт"},
		{"лишняя скобка", "x = 1\n}\n", "строка 2: лишняя"},
		{"for без in", "for i 1..3 {\n}\n", "неверный синтаксис for"},
		{"for без скобки", "for i in 1..3\n    x = 1\n", "строка 1: после for ожидается '{'"},
		{"sleep без скобок", "sleep 1\n", "неверный синтаксис sleep"},
		{"sleep без аргумента", "sleep()\n", "неверный синтаксис sleep"},
		{"неизвестный метод в теле", "for i in 1..3 {\n    Nope(SESSION_ID)\n}\n", "строка 2: метод 'Nope' не найден"},
	}

	for _, c := range cases {
		_, err := NewParser(data.Methods).Parse(c.text)
		if err == nil {
			t.Errorf("%s: ожидалась ошибка, получили nil", c.name)
			continue
		}

		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %q не содержит %q", c.name, err.Error(), c.want)
		}
	}
}

// Прежний синтаксис сценариев должен разбираться как раньше
func TestParseKeepsOldSyntax(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
# комментарий
// тоже комментарий
sdp = "v=0\r\ns=-\r\n"
resp = Echo(SESSION_ID, 5)
Echo(SESSION_ID, resp.channelId)
sleep_ms = 5
`)

	if len(s.Commands) != 4 {
		t.Fatalf("команд: %d, ожидалось 4", len(s.Commands))
	}

	if !s.Commands[0].IsSimpleAssign || s.Commands[0].VarName != "sdp" {
		t.Errorf("присваивание строки разобрано неверно: %+v", s.Commands[0])
	}

	call := s.Commands[1]
	if !call.IsFunctionCall || call.Name != "Echo" || len(call.Args) != 2 || call.Args[0].Name != "session_id" {
		t.Errorf("вызов с присваиванием разобран неверно: %+v", call)
	}

	if s.Commands[2].Name != "Echo" || s.Commands[2].IsFunctionCall {
		t.Errorf("вызов без присваивания разобран неверно: %+v", s.Commands[2])
	}

	// переменная с именем, начинающимся на sleep, — это обычная переменная, а не пауза
	if s.Commands[3].IsSleep || !s.Commands[3].IsSimpleAssign {
		t.Errorf("sleep_ms = 5 принято за паузу: %+v", s.Commands[3])
	}
}

// ===== Выполнение =====

func TestExecuteFor(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "for i in 1..3 {\n    x = i\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "x"); got != "3" {
		t.Errorf("x = %q, ожидалось \"3\"", got)
	}

	if executor.executed != 3 {
		t.Errorf("выполнено команд: %d, ожидалось 3", executor.executed)
	}
}

func TestExecuteNestedFor(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "for i in 1..2 {\n    for j in 1..3 {\n        n = j\n    }\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if executor.executed != 6 {
		t.Errorf("выполнено команд: %d, ожидалось 6", executor.executed)
	}

	if got, _ := varValue(executor, "n"); got != "3" {
		t.Errorf("n = %q, ожидалось \"3\"", got)
	}
}

func TestExecuteForBoundsFromVariable(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "n = 4\nfor i in 1..n {\n    last = i\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "last"); got != "4" {
		t.Errorf("last = %q, ожидалось \"4\"", got)
	}
}

func TestExecuteForEmptyRange(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "for i in 3..1 {\n    x = 1\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, ok := varValue(executor, "x"); ok {
		t.Error("тело цикла с пустым диапазоном не должно выполняться")
	}
}

func TestExecuteStopsOnErrorInLoop(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "for i in 1..3 {\n    x = 1\n    y = nope\n}\nz = 1\n")

	err := executor.Execute(s)
	if err == nil {
		t.Fatal("ожидалась ошибка из тела цикла")
	}

	if !strings.Contains(err.Error(), "команде 1.2") {
		t.Errorf("в ошибке нет номера команды 1.2: %v", err)
	}

	if executor.executed != 2 {
		t.Errorf("выполнено команд: %d, ожидалось 2 (остановка на первой ошибке)", executor.executed)
	}

	if _, ok := varValue(executor, "z"); ok {
		t.Error("команда после цикла не должна выполняться после остановки")
	}
}

func TestExecuteContinueOnErrorInLoop(t *testing.T) {
	executor, data := newBareExecutor(t)
	executor.ContinueOnError = true

	s := mustParse(t, data, "for i in 1..3 {\n    x = 1\n    y = nope\n}\nz = 1\n")

	err := executor.Execute(s)
	if err == nil || !strings.Contains(err.Error(), "команд с ошибками: 3 из 7") {
		t.Fatalf("ожидалась ошибка «команд с ошибками: 3 из 7», получили: %v", err)
	}

	if got, _ := varValue(executor, "z"); got != "1" {
		t.Errorf("z = %q, ожидалось \"1\": после ошибок сценарий должен дойти до конца", got)
	}
}

func TestExecuteBadBounds(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"неизвестная переменная", "for i in nope..3 {\n    x = 1\n}\n", "начало диапазона"},
		{"не целое число", "for i in 1..2.5 {\n    x = 1\n}\n", "целое число"},
	}

	for _, c := range cases {
		executor, data := newBareExecutor(t)

		err := executor.Execute(mustParse(t, data, c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

func TestExecuteSleep(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "d = 0.05\nsleep(d)\nsleep(0.05)\n")

	start := time.Now()

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("две паузы по 50 мс заняли %v", elapsed)
	}

	if executor.executed != 3 {
		t.Errorf("выполнено команд: %d, ожидалось 3", executor.executed)
	}
}

func TestExecuteSleepErrors(t *testing.T) {
	for _, text := range []string{"sleep(-1)\n", "sleep(abc)\n", "sleep(100000)\n"} {
		executor, data := newBareExecutor(t)

		err := executor.Execute(mustParse(t, data, text))
		if err == nil || !strings.Contains(err.Error(), "sleep") {
			t.Errorf("%q: ожидалась ошибка sleep, получили: %v", strings.TrimSpace(text), err)
		}
	}
}
