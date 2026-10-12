package scenario

import (
	"strings"
	"testing"
)

// ===== Разбор log и if =====

func TestParseLog(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "log(\"channel:\", resp.channelId)\nlog (\"a, b\")\nlog(x)\n")

	if len(s.Commands) != 3 {
		t.Fatalf("команд: %d, ожидалось 3", len(s.Commands))
	}

	first := s.Commands[0]
	if !first.IsLog || len(first.LogArgs) != 2 || first.LogArgs[0] != `"channel:"` || first.LogArgs[1] != "resp.channelId" {
		t.Errorf("log с двумя аргументами разобран неверно: %+v", first)
	}

	// запятая внутри кавычек не делит аргументы
	if second := s.Commands[1]; len(second.LogArgs) != 1 || second.LogArgs[0] != `"a, b"` {
		t.Errorf("запятая в кавычках разделила аргументы: %+v", second.LogArgs)
	}
}

func TestParseLogErrors(t *testing.T) {
	data := loadTestProto(t)

	for _, text := range []string{"log\n", "log()\n", "log(\"a\", , \"b\")\n", "log \"a\"\n"} {
		if _, err := NewParser(data.Methods).Parse(text); err == nil || !strings.Contains(err.Error(), "log") {
			t.Errorf("%q: ожидалась ошибка про log, получили: %v", strings.TrimSpace(text), err)
		}
	}
}

// Переменные с именами log, if, else — обычные переменные, а не ключевые слова
func TestParseKeywordNamesAsVariables(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "log = 1\nif = 2\nlogger = 3\niffy = 4\n")

	if len(s.Commands) != 4 {
		t.Fatalf("команд: %d, ожидалось 4", len(s.Commands))
	}

	for i, cmd := range s.Commands {
		if !cmd.IsSimpleAssign || cmd.IsLog || cmd.IsIf {
			t.Errorf("команда %d принята за ключевое слово: %+v", i+1, cmd)
		}
	}
}

func TestParseIf(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
if resp.replyCode == RC_SUCCESS {
    a = 1
} else {
    b = 2
    c = 3
}
d = 4
`)

	if len(s.Commands) != 2 {
		t.Fatalf("команд верхнего уровня: %d, ожидалось 2", len(s.Commands))
	}

	cond := s.Commands[0]
	if !cond.IsIf || cond.CondLeft != "resp.replyCode" || cond.CondOp != "==" || cond.CondRight != "RC_SUCCESS" {
		t.Fatalf("неверно разобрано условие: %+v", cond)
	}

	if len(cond.Body) != 1 || len(cond.ElseBody) != 2 {
		t.Errorf("команд в then: %d (ожидалось 1), в else: %d (ожидалось 2)", len(cond.Body), len(cond.ElseBody))
	}

	if !s.Commands[1].IsSimpleAssign || s.Commands[1].VarName != "d" {
		t.Errorf("команда после if разобрана неверно: %+v", s.Commands[1])
	}
}

func TestParseIfOperators(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		cond, left, op, right string
	}{
		{"a == 1", "a", "==", "1"},
		{"a!=1", "a", "!=", "1"},
		{"a < 1", "a", "<", "1"},
		{"a > 1", "a", ">", "1"},
		{"a <= 1", "a", "<=", "1"},
		{"a>=1", "a", ">=", "1"},
		{`name == "a == b"`, "name", "==", `"a == b"`},
		{`"x<y" != s`, `"x<y"`, "!=", "s"},
	}

	for _, c := range cases {
		s := mustParse(t, data, "if "+c.cond+" {\n}\n")

		cmd := s.Commands[0]
		if cmd.CondLeft != c.left || cmd.CondOp != c.op || cmd.CondRight != c.right {
			t.Errorf("%q: получили %q %s %q, ожидалось %q %s %q", c.cond, cmd.CondLeft, cmd.CondOp, cmd.CondRight, c.left, c.op, c.right)
		}
	}
}

func TestParseNestedIfAndFor(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, `
for i in 1..3 {
    if i == 2 {
        if i > 1 {
            x = 1
        }
    } else {
        y = 2
    }
}
`)

	loop := s.Commands[0]
	if len(loop.Body) != 1 || !loop.Body[0].IsIf {
		t.Fatalf("в теле цикла ожидался if: %+v", loop.Body)
	}

	outer := loop.Body[0]
	if len(outer.Body) != 1 || !outer.Body[0].IsIf || len(outer.ElseBody) != 1 {
		t.Errorf("неверная структура вложенных if: then=%d else=%d", len(outer.Body), len(outer.ElseBody))
	}
}

func TestParseIfErrors(t *testing.T) {
	data := loadTestProto(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"if не закрыт", "if a == 1 {\n    x = 1\n", "строка 1: if не закрыт"},
		{"else не закрыт", "if a == 1 {\n} else {\n    x = 1\n", "строка 2: else не закрыт"},
		{"else без if", "x = 1\n} else {\n}\n", "строка 2: else без соответствующего if"},
		{"else в теле цикла", "for i in 1..2 {\n} else {\n}\n", "else без соответствующего if"},
		{"else if", "if a == 1 {\n} else if a == 2 {\n}\n", "допустим только else"},
		{"if без скобки", "if a == 1\n    x = 1\n", "строка 1: после if ожидается '{'"},
		{"else без скобки", "if a == 1\n{\n}\nelse\n    x = 1\n", "строка 4: после else ожидается '{'"},
		{"заголовок for в конце файла без тела", "for i in 1..2\n", "после for ожидается '{'"},
		{"else с новой строки после for", "for i in 1..2 {\n}\nelse {\n}\n", "else без соответствующего if"},
		{"нет оператора", "if a {\n}\n", "нет оператора сравнения"},
		{"одиночное =", "if a = 1 {\n}\n", "неизвестный оператор '='"},
		{"два оператора", "if a == 1 == 2 {\n}\n", "один оператор сравнения"},
		{"&&", "if a == 1 && b == 2 {\n}\n", "в условии '&&' не поддерживается"},
		{"||", "if a == 1 || b == 2 {\n}\n", "в условии '||' не поддерживается"},
		{"нет правой части", "if a == {\n}\n", "не хватает значения"},
		{"неизвестный блок", "while a {\n}\n", "неизвестная конструкция"},
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

// ===== Выполнение =====

func TestExecuteIfElse(t *testing.T) {
	cases := []struct {
		cond string
		want string
	}{
		{"1 == 1", "then"},
		{"1 == 2", "else"},
		{"1 != 2", "then"},
		{"2 < 10", "then"}, // число, а не строка: "2" < "10" как строки было бы ложью
		{"10 > 9", "then"},
		{"2 <= 2", "then"},
		{"3 >= 4", "else"},
		{"1.0 == 1", "then"}, // числа сравниваются по значению
		{`"abc" == "abc"`, "then"},
		{`"abc" != "abd"`, "then"},
		{"72057594037928107 < 72057594037928108", "then"}, // больше 2^53: float64 их не различил бы
		{"72057594037928107 == 72057594037928108", "else"},
	}

	for _, c := range cases {
		executor, data := newBareExecutor(t)

		s := mustParse(t, data, "if "+c.cond+" {\n    r = \"then\"\n} else {\n    r = \"else\"\n}\n")

		if err := executor.Execute(s); err != nil {
			t.Errorf("%s: Execute: %v", c.cond, err)
			continue
		}

		if got, _ := varValue(executor, "r"); got != c.want {
			t.Errorf("if %s: выполнилась ветка %q, ожидалась %q", c.cond, got, c.want)
		}
	}
}

func TestExecuteIfWithoutElse(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "if 1 == 2 {\n    x = 1\n}\ny = 2\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, ok := varValue(executor, "x"); ok {
		t.Error("ветка then не должна выполняться при ложном условии")
	}

	if got, _ := varValue(executor, "y"); got != "2" {
		t.Errorf("y = %q, ожидалось \"2\"", got)
	}
}

// Ответ MM хранится как JSON, где enum записан именем; в условии он сравнивается и с именем, и с числом
func TestExecuteIfEnumFromResponse(t *testing.T) {
	executor, data := newBareExecutor(t)

	executor.exprParser.vars.Set("resp", `{"replyCode":"RC_FAIL","channelId":"72057594037928107"}`)

	s := mustParse(t, data, `
if resp.replyCode == RC_FAIL {
    a = "fail"
}
if resp.replyCode != RC_SUCCESS {
    b = "not success"
}
if resp.replyCode == 1 {
    c = "by number"
}
if resp.replyCode == RC_SUCCESS {
    d = "must not run"
}
if resp.channelId > 72057594037927936 {
    e = "id"
}
`)

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for name, want := range map[string]string{"a": "fail", "b": "not success", "c": "by number", "e": "id"} {
		if got, _ := varValue(executor, name); got != want {
			t.Errorf("%s = %q, ожидалось %q", name, got, want)
		}
	}

	if _, ok := varValue(executor, "d"); ok {
		t.Error("ветка с RC_SUCCESS не должна выполняться при replyCode=RC_FAIL")
	}
}

// Строка в кавычках остаётся строкой, даже если совпадает с именем enum
func TestExecuteIfQuotedEnumNameStaysString(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "if \"RC_SUCCESS\" == 0 {\n    x = 1\n}\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, ok := varValue(executor, "x"); ok {
		t.Error("строка \"RC_SUCCESS\" не должна равняться числу 0")
	}
}

func TestExecuteIfInsideFor(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, `
evens = 0
odds = 0
for i in 1..5 {
    if i == 2 {
        evens = i
    } else {
        odds = i
    }
}
`)

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "evens"); got != "2" {
		t.Errorf("evens = %q, ожидалось \"2\"", got)
	}

	// последняя итерация (i=5) попала в else
	if got, _ := varValue(executor, "odds"); got != "5" {
		t.Errorf("odds = %q, ожидалось \"5\"", got)
	}
}

func TestExecuteIfErrors(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"неизвестная переменная слева", "if nope == 1 {\n}\n", "левая часть"},
		{"неизвестная переменная справа", "if 1 == nope {\n}\n", "правая часть"},
		{"порядок у строк", "if \"a\" < \"b\" {\n}\n", "применим только к числам"},
	}

	for _, c := range cases {
		executor, data := newBareExecutor(t)

		err := executor.Execute(mustParse(t, data, c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "команде 1") {
			t.Errorf("%s: ожидалась ошибка с %q и номером команды 1, получили: %v", c.name, c.want, err)
		}
	}
}

func TestExecuteIfErrorContinues(t *testing.T) {
	executor, data := newBareExecutor(t)
	executor.ContinueOnError = true

	s := mustParse(t, data, "if nope == 1 {\n    x = 1\n}\ny = 2\n")

	err := executor.Execute(s)
	if err == nil || !strings.Contains(err.Error(), "команд с ошибками: 1 из 2") {
		t.Fatalf("ожидалась ошибка «команд с ошибками: 1 из 2», получили: %v", err)
	}

	if got, _ := varValue(executor, "y"); got != "2" {
		t.Errorf("y = %q: после ошибки в условии сценарий должен дойти до конца", got)
	}
}

func TestExecuteErrorInsideIfBranch(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, "if 1 == 1 {\n    ok = 1\n    bad = nope\n}\nz = 1\n")

	err := executor.Execute(s)
	if err == nil || !strings.Contains(err.Error(), "команде 1.2") {
		t.Fatalf("ожидалась ошибка в команде 1.2, получили: %v", err)
	}

	if _, ok := varValue(executor, "z"); ok {
		t.Error("после ошибки в ветке if сценарий должен остановиться")
	}
}

func TestExecuteLog(t *testing.T) {
	executor, data := newBareExecutor(t)

	executor.exprParser.vars.Set("resp", `{"channelId":"72057594037928107"}`)

	s := mustParse(t, data, "n = 5\nlog(\"channel:\", resp.channelId, \"n =\", n, RC_FAIL)\n")

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if executor.executed != 2 {
		t.Errorf("выполнено команд: %d, ожидалось 2", executor.executed)
	}
}

func TestFormatLog(t *testing.T) {
	executor, data := newBareExecutor(t)

	executor.exprParser.vars.Set("resp", `{"channelId":"72057594037928107"}`)
	executor.exprParser.vars.Set("n", "5")

	s := mustParse(t, data, "log(\"channel:\", resp.channelId, \"n =\", n)\n")

	got, err := executor.formatLog(s.Commands[0])
	if err != nil {
		t.Fatalf("formatLog: %v", err)
	}

	if want := "channel: 72057594037928107 n = 5"; got != want {
		t.Errorf("строка лога %q, ожидалась %q", got, want)
	}
}

func TestExecuteLogUnknownVariable(t *testing.T) {
	executor, data := newBareExecutor(t)

	err := executor.Execute(mustParse(t, data, "log(\"x\", nope)\n"))
	if err == nil || !strings.Contains(err.Error(), "log: аргумент 2") {
		t.Errorf("ожидалась ошибка «log: аргумент 2», получили: %v", err)
	}
}

func TestCompareValues(t *testing.T) {
	cases := []struct {
		left, op, right string
		want            bool
		wantErr         bool
	}{
		{"1e3", "==", "1000", true, false},
		{"-5", "<", "3", true, false},
		{"0.1", "<", "0.2", true, false},
		{"abc", "==", "abc", true, false},
		{"abc", "==", "1", false, false}, // смесь строки и числа — сравнение строк
		{"inf", "==", "inf", true, false},
		{"NaN", "==", "NaN", true, false}, // как строки: числом не считаются
		{"abc", "<", "abd", false, true},
		{"1", "<", "abc", false, true},
		{"1e999999999", "==", "1e999999999", true, false},
	}

	for _, c := range cases {
		got, err := compareValues(c.left, c.op, c.right)

		if (err != nil) != c.wantErr {
			t.Errorf("%s %s %s: ошибка %v, ожидалась ошибка=%v", c.left, c.op, c.right, err, c.wantErr)
			continue
		}

		if err == nil && got != c.want {
			t.Errorf("%s %s %s = %v, ожидалось %v", c.left, c.op, c.right, got, c.want)
		}
	}
}

// ===== Скобка на следующей строке =====

// Разные стили записи одного и того же сценария должны давать одну и ту же структуру команд
func TestParseBraceStyles(t *testing.T) {
	data := loadTestProto(t)

	styles := map[string]string{
		"К&Р":             "for i in 1..3 {\n    if i == 2 {\n        a = 1\n    } else {\n        b = 2\n    }\n}\nz = 1\n",
		"Allman":          "for i in 1..3\n{\n    if i == 2\n    {\n        a = 1\n    }\n    else\n    {\n        b = 2\n    }\n}\nz = 1\n",
		"смешанный":       "for i in 1..3\n{\n    if i == 2 {\n        a = 1\n    }\n    else {\n        b = 2\n    }\n}\nz = 1\n",
		"} else и { ниже": "for i in 1..3 {\n    if i == 2 {\n        a = 1\n    } else\n    {\n        b = 2\n    }\n}\nz = 1\n",
		"комментарии и пустые строки": "for i in 1..3\n\n# открываем\n{\n    if i == 2\n    // условие\n    {\n        a = 1\n    }\n\n    # иначе\n    else\n    {\n        b = 2\n    }\n}\nz = 1\n",
	}

	for name, text := range styles {
		s := mustParse(t, data, text)

		if len(s.Commands) != 2 || !s.Commands[0].IsFor || s.Commands[1].VarName != "z" {
			t.Errorf("%s: неверная структура верхнего уровня: %+v", name, s.Commands)
			continue
		}

		loop := s.Commands[0]
		if len(loop.Body) != 1 || !loop.Body[0].IsIf {
			t.Errorf("%s: в теле цикла ожидался if: %+v", name, loop.Body)
			continue
		}

		cond := loop.Body[0]
		if cond.CondLeft != "i" || cond.CondRight != "2" ||
			len(cond.Body) != 1 || cond.Body[0].VarName != "a" ||
			len(cond.ElseBody) != 1 || cond.ElseBody[0].VarName != "b" {
			t.Errorf("%s: неверно разобран if: %+v", name, cond)
		}
	}
}

func TestExecuteAllmanStyle(t *testing.T) {
	executor, data := newBareExecutor(t)

	s := mustParse(t, data, `
for i in 1..4
{
    if i < 3
    {
        small = i
    }
    else
    {
        big = i
    }
}
`)

	if err := executor.Execute(s); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, _ := varValue(executor, "small"); got != "2" {
		t.Errorf("small = %q, ожидалось \"2\"", got)
	}

	if got, _ := varValue(executor, "big"); got != "4" {
		t.Errorf("big = %q, ожидалось \"4\"", got)
	}
}

// После if без else следующая команда не должна приниматься за ветку else
func TestParseIfFollowedByUnrelatedCommand(t *testing.T) {
	data := loadTestProto(t)

	s := mustParse(t, data, "if 1 == 1\n{\n    a = 1\n}\n\n# комментарий\nelse_value = 5\n")

	if len(s.Commands) != 2 || len(s.Commands[0].ElseBody) != 0 || s.Commands[1].VarName != "else_value" {
		t.Errorf("переменная else_value принята за else: %+v", s.Commands)
	}
}
