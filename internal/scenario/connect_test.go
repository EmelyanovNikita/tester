package scenario

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

// fakeConnector считает вызовы и возвращает «соединение» без реальной сети
type fakeConnector struct {
	calls int
	err   error
}

func (f *fakeConnector) connect() (*grpc.ClientConn, string, error) {
	f.calls++

	if f.err != nil {
		return nil, "", f.err
	}

	return &grpc.ClientConn{}, "sid-1", nil
}

func TestParseConnect(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "connect()\nconnect ( )\n")

	if len(s.Commands) != 2 || !s.Commands[0].IsConnect || !s.Commands[1].IsConnect {
		t.Fatalf("connect() разобран неверно: %+v", s.Commands)
	}
}

func TestParseConnectErrors(t *testing.T) {
	data := loadTestProto(t)

	for _, text := range []string{"connect\n", "connect(1)\n", "connect(\n", "connect 5\n"} {
		_, err := NewParser(data.Methods).Parse(text)
		if err == nil || !strings.Contains(err.Error(), "connect") {
			t.Errorf("%q: ожидалась ошибка про connect, получили: %v", strings.TrimSpace(text), err)
		}
	}
}

func TestParseConnectAsVariableName(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "connect = 1\nconnected = 2\n")

	for i, cmd := range s.Commands {
		if cmd.IsConnect || !cmd.IsSimpleAssign {
			t.Errorf("команда %d принята за connect(): %+v", i+1, cmd)
		}
	}
}

func TestUsesConnect(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want bool
	}{
		{"нет connect", "x = 1\nfor i in 1..2 {\n    y = i\n}\n", false},
		{"в начале", "connect()\nx = 1\n", true},
		{"в цикле", "for i in 1..2 {\n    connect()\n}\n", true},
		{"в ветке then", "if 1 == 1 {\n    connect()\n}\n", true},
		{"в ветке else", "if 1 == 2 {\n    x = 1\n} else {\n    connect()\n}\n", true},
		{"глубоко вложенный", "for i in 1..2 {\n    if i == 1 {\n        for j in 1..2 {\n            connect()\n        }\n    }\n}\n", true},
	}

	for _, c := range cases {
		if got := mustParse(t, data, c.text).UsesConnect(); got != c.want {
			t.Errorf("%s: UsesConnect = %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

func TestExecuteConnectOnce(t *testing.T) {
	executor, data := newBareExecutor(t)

	fake := &fakeConnector{}
	executor.SetConnector(fake.connect)

	s := mustParse(t, data, "connect()\nconnect()\nid = SESSION_ID\nfor i in 1..3 {\n    connect()\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.calls != 1 {
		t.Errorf("соединение устанавливалось %d раз, ожидалось 1: повторные connect() должны пропускаться", fake.calls)
	}

	if got, _ := varValue(executor, "id"); got != "sid-1" {
		t.Errorf("SESSION_ID после connect() = %q, ожидалось \"sid-1\"", got)
	}
}

func TestExecuteConnectSkippedWhenAlreadyConnected(t *testing.T) {
	data := loadTestProto(t)
	fake := &fakeConnector{}

	executor := NewExecutor(&grpc.ClientConn{}, data.Methods, "already", data.Enums)
	executor.SetConnector(fake.connect)

	if err := executor.Execute(mustParse(t, data, "connect()\n")); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if fake.calls != 0 {
		t.Errorf("connector вызван %d раз, хотя соединение уже было", fake.calls)
	}

	if got, _ := varValue(executor, "SESSION_ID"); got != "already" {
		t.Errorf("SESSION_ID = %q, ожидалось \"already\"", got)
	}
}

func TestSessionIDUndefinedBeforeConnect(t *testing.T) {
	executor, data := newBareExecutor(t)

	// executor из newBareExecutor создан с sessionID, поэтому проверяем исполнитель без него
	executor = NewExecutor(nil, data.Methods, "", data.Enums)

	err := executor.Execute(mustParse(t, data, "x = SESSION_ID\n"))
	if err == nil || !strings.Contains(err.Error(), "SESSION_ID") {
		t.Fatalf("ожидалась ошибка про SESSION_ID до connect(), получили: %v", err)
	}
}

func TestExecuteConnectErrors(t *testing.T) {
	data := loadTestProto(t)

	// connector не настроен
	executor := NewExecutor(nil, data.Methods, "", data.Enums)

	err := executor.Execute(mustParse(t, data, "connect()\n"))
	if err == nil || !strings.Contains(err.Error(), "не настроено") {
		t.Errorf("без connector: ожидалась ошибка «не настроено», получили: %v", err)
	}

	// connector вернул ошибку
	fake := &fakeConnector{err: errors.New("MM недоступен")}
	executor = NewExecutor(nil, data.Methods, "", data.Enums)
	executor.SetConnector(fake.connect)

	err = executor.Execute(mustParse(t, data, "connect()\nx = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "подключение к MM") || !strings.Contains(err.Error(), "MM недоступен") || !strings.Contains(err.Error(), "команде 1") {
		t.Errorf("ошибка подключения: %v", err)
	}

	if _, ok := varValue(executor, "x"); ok {
		t.Error("после неудачного connect() сценарий должен остановиться")
	}
}

func TestEnsureConnected(t *testing.T) {
	data := loadTestProto(t)
	fake := &fakeConnector{}

	executor := NewExecutor(nil, data.Methods, "", data.Enums)
	executor.SetConnector(fake.connect)

	connected, err := executor.EnsureConnected()
	if err != nil || !connected {
		t.Fatalf("первый вызов: connected=%v err=%v", connected, err)
	}

	connected, err = executor.EnsureConnected()
	if err != nil || connected {
		t.Fatalf("повторный вызов: connected=%v err=%v, ожидалось false без ошибки", connected, err)
	}

	if fake.calls != 1 {
		t.Errorf("connector вызван %d раз, ожидалось 1", fake.calls)
	}
}
