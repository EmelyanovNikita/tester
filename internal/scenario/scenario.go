package scenario

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"tester_mm/internal/protoloader"
)

// Argument - один аргумент команды
type Argument struct {
	Name  string
	Value string
}

// Command - одна команда сценария
type Command struct {
	Name    string
	Args    []Argument
	Method  protoloader.MethodInfo
	RawLine string

	// Для присваиваний
	VarName        string // имя переменной слева от =
	RightPart      string // что справа от =
	IsFunctionCall bool   // true если справа функция (с "(")
	IsSimpleAssign bool   // true если простое присваивание (без "(")
	Value          string // значение для простого присваивания

	// Для цикла: for i in 1..10 { ... }
	IsFor    bool      // true если это цикл
	LoopVar  string    // имя переменной цикла
	LoopFrom string    // начало диапазона: число или переменная
	LoopTo   string    // конец диапазона (включительно): число или переменная
	Body     []Command // команды внутри { }

	// Для паузы: sleep(секунды)
	IsSleep  bool   // true если это пауза
	SleepArg string // секунды: число или переменная

	// Для подключения к MM: connect()
	IsConnect bool

	// Для ожидания события: [ev =] wait_event(Имя, поле == значение, ..., таймаут); результат попадает в VarName
	IsWaitEvent  bool
	EventName    string           // имя события (метод proto, который MM вызывает на тестере)
	EventConds   []EventCondition // условия на поля события; все должны выполняться
	EventTimeout string           // таймаут в секундах: число или переменная; пусто — по умолчанию

	// Для RTP: rtp = rtp_open(...), rtp_start(rtp, sdp, ...), rtp_stats(rtp), rtp_stop(rtp)
	IsRTP   bool
	RTPOp   string      // rtp_open, rtp_start, rtp_dtmf, rtp_stats, rtp_stop
	RTPArgs []string    // позиционные аргументы: выражения сценария
	RTPOpts []RTPOption // именованные: tone = 440

	// Для вывода в лог: log(a, b, ...); fail(a, b, ...) — то же, но команда завершается ошибкой (звонок считается проваленным)
	IsLog   bool
	IsFail  bool
	LogArgs []string // выражения-аргументы, каждое вычисляется при выполнении

	// Для условия: if a == b { ... } else { ... }; тело ветки "then" лежит в Body
	IsIf      bool
	CondLeft  string    // левый операнд: число, строка в кавычках, переменная, поле или enum
	CondOp    string    // ==, !=, <, >, <=, >=
	CondRight string    // правый операнд
	ElseBody  []Command // команды ветки else (пусто, если else нет)
}

// Заголовок цикла: for i in 1..10 {   (границы — число или переменная, пробелы вокруг .. необязательны).
// Скобка может стоять на следующей строке, поэтому в заголовке она необязательна.
var forHeaderRe = regexp.MustCompile(`^for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(\S+?)\s*\.\.\s*(\S+?)\s*\{?$`)

// Пауза: sleep(0.5)
var sleepRe = regexp.MustCompile(`^sleep\s*\((.*)\)$`)

// Подключение к MM: connect()
var connectRe = regexp.MustCompile(`^connect\s*\(\s*\)$`)

// Вывод в лог: log("channel:", resp.channelId)
// logLikeRe — вызовы log(...) и fail(...)
var logLikeRe = map[string]*regexp.Regexp{
	"log":  regexp.MustCompile(`^log\s*\((.*)\)$`),
	"fail": regexp.MustCompile(`^fail\s*\((.*)\)$`),
}

// Заголовок условия: if resp.replyCode == RC_SUCCESS {   (скобка может стоять на следующей строке)
var ifHeaderRe = regexp.MustCompile(`^if\s+(.+?)\s*\{?$`)

// Конец ветки then и начало else: "} else {" или "} else" (скобка на следующей строке)
var elseRe = regexp.MustCompile(`^\}\s*else\s*\{?$`)

// else на отдельной строке после "}" с новой строки
var elseLineRe = regexp.MustCompile(`^else\s*\{?$`)

// EventCondition — условие на поле события в wait_event: Field Op Value
type EventCondition struct {
	Field string // путь к полю события (channelId, info.type)
	Op    string // ==, !=, <, >, <=, >=
	Value string // выражение сценария, вычисляется при выполнении
}

// Ожидание события: wait_event(AudioFilePlayed, channelId == resp.channelId, 15)
var waitEventRe = regexp.MustCompile(`^wait_event\s*\((.*)\)$`)

// Путь к полю события: имя или имена через точку
var eventFieldRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// Имя события: идентификатор, в кавычках или без
var eventNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Scenario - полный сценарий
type Scenario struct {
	Commands []Command
}

// Parser парсит сценарий
type Parser struct {
	methods map[string]protoloader.MethodInfo
}

func NewParser(methods map[string]protoloader.MethodInfo) *Parser {
	return &Parser{methods: methods}
}

// Parse разбирает текст сценария
func (parser *Parser) Parse(text string) (*Scenario, error) {
	lines := strings.Split(text, "\n")

	commands, _, _, err := parser.parseBlock(lines, 0, blockTop, 0)
	if err != nil {
		return nil, err
	}

	return &Scenario{Commands: commands}, nil
}

// blockKind — какой блок сейчас разбирается; от него зависит, чем он может закончиться
type blockKind int

const (
	blockTop  blockKind = iota // верхний уровень сценария, читаем до конца текста
	blockFor                   // тело цикла for, заканчивается "}"
	blockIf                    // ветка then, заканчивается "}" или "} else {"
	blockElse                  // ветка else, заканчивается "}"
)

// blockEnd — чем закончился разобранный блок
type blockEnd int

const (
	endText  blockEnd = iota // дошли до конца текста
	endClose                 // встретили "}"
	endElse                  // встретили "} else {"
)

// unclosedMessage — текст ошибки для блока, который не закрыт до конца текста
func (kind blockKind) unclosedMessage(openLine int) string {
	switch kind {
	case blockFor:
		return fmt.Sprintf("строка %d: цикл for не закрыт, не хватает '}'", openLine)
	case blockIf:
		return fmt.Sprintf("строка %d: if не закрыт, не хватает '}'", openLine)
	default:
		return fmt.Sprintf("строка %d: else не закрыт, не хватает '}'", openLine)
	}
}

// parseBlock разбирает строки начиная с индекса start.
// kind — какой блок разбираем, openLine — строка (с 1), на которой он открыт (для сообщений об ошибках).
// Возвращает команды, индекс строки, с которой нужно продолжать разбор, и причину окончания блока.
func (parser *Parser) parseBlock(lines []string, start int, kind blockKind, openLine int) ([]Command, int, blockEnd, error) {
	var commands []Command

	for i := start; i < len(lines); i++ {
		lineNum := i + 1
		line := strings.TrimSpace(lines[i])

		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		// Конец блока
		if line == "}" {
			if kind == blockTop {
				return nil, 0, endText, fmt.Errorf("строка %d: лишняя '}' без открывающего for или if", lineNum)
			}

			return commands, i + 1, endClose, nil
		}

		// "} else {" — конец ветки then и начало ветки else
		if elseRe.MatchString(line) {
			if kind != blockIf {
				return nil, 0, endText, fmt.Errorf("строка %d: else без соответствующего if", lineNum)
			}

			return commands, i + 1, endElse, nil
		}

		if strings.HasPrefix(line, "}") {
			return nil, 0, endText, fmt.Errorf("строка %d: после '}' допустим только else: '} else {'", lineNum)
		}

		if startsWithKeyword(line, "else") {
			return nil, 0, endText, fmt.Errorf("строка %d: else без соответствующего if: он допустим только сразу после '}' ветки if", lineNum)
		}

		// Начало цикла
		if startsWithKeyword(line, "for") {
			cmd, next, err := parser.parseFor(lines, i, line)
			if err != nil {
				return nil, 0, endText, err
			}

			commands = append(commands, cmd)

			// next — строка после закрывающей "}"; после i++ в цикле продолжим именно с неё
			i = next - 1

			continue
		}

		// Начало условия
		if startsWithKeyword(line, "if") {
			cmd, next, err := parser.parseIf(lines, i, line)
			if err != nil {
				return nil, 0, endText, err
			}

			commands = append(commands, cmd)
			i = next - 1

			continue
		}

		// Строка открывает блок, но это не for и не if: иначе её тело молча выполнилось бы как обычные команды
		if strings.HasSuffix(line, "{") {
			return nil, 0, endText, fmt.Errorf("строка %d: неизвестная конструкция с '{': %s", lineNum, line)
		}

		cmd, ok, err := parser.parseLine(line, lineNum)
		if err != nil {
			return nil, 0, endText, err
		}

		if ok {
			commands = append(commands, cmd)
		}
	}

	if kind != blockTop {
		return nil, 0, endText, fmt.Errorf("%s", kind.unclosedMessage(openLine))
	}

	return commands, len(lines), endText, nil
}

// parseFor разбирает цикл, заголовок которого находится в lines[headerIdx].
// Возвращает команду и индекс строки после закрывающей "}".
func (parser *Parser) parseFor(lines []string, headerIdx int, header string) (Command, int, error) {
	lineNum := headerIdx + 1

	m := forHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return Command{}, 0, fmt.Errorf("строка %d: неверный синтаксис for, ожидается: for i in 1..10 {", lineNum)
	}

	bodyStart, err := openBrace(lines, headerIdx, "for")
	if err != nil {
		return Command{}, 0, err
	}

	body, next, _, err := parser.parseBlock(lines, bodyStart, blockFor, lineNum)
	if err != nil {
		return Command{}, 0, err
	}

	return Command{
		RawLine:  header,
		IsFor:    true,
		LoopVar:  m[1],
		LoopFrom: m[2],
		LoopTo:   m[3],
		Body:     body,
	}, next, nil
}

// parseIf разбирает условие вместе с веткой else, если она есть.
// Возвращает команду и индекс строки после последней закрывающей "}".
func (parser *Parser) parseIf(lines []string, headerIdx int, header string) (Command, int, error) {
	lineNum := headerIdx + 1

	m := ifHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return Command{}, 0, fmt.Errorf("строка %d: неверный синтаксис if, ожидается: if a == b {", lineNum)
	}

	left, op, right, err := parseCondition(m[1])
	if err != nil {
		return Command{}, 0, fmt.Errorf("строка %d: %v", lineNum, err)
	}

	cmd := Command{
		RawLine:   header,
		IsIf:      true,
		CondLeft:  left,
		CondOp:    op,
		CondRight: right,
	}

	bodyStart, err := openBrace(lines, headerIdx, "if")
	if err != nil {
		return Command{}, 0, err
	}

	body, next, end, err := parser.parseBlock(lines, bodyStart, blockIf, lineNum)
	if err != nil {
		return Command{}, 0, err
	}

	cmd.Body = body

	// "} else {" — else уже встретился в той же строке, что и закрывающая скобка
	elseIdx := next - 1

	// "}" и затем else с новой строки: смотрим на ближайшую значимую строку
	if end == endClose {
		if j, ok := nextSignificantLine(lines, next); ok && elseLineRe.MatchString(strings.TrimSpace(lines[j])) {
			elseIdx = j
			end = endElse
		}
	}

	if end == endElse {
		elseStart, err := openBrace(lines, elseIdx, "else")
		if err != nil {
			return Command{}, 0, err
		}

		cmd.ElseBody, next, _, err = parser.parseBlock(lines, elseStart, blockElse, elseIdx+1)
		if err != nil {
			return Command{}, 0, err
		}
	}

	return cmd, next, nil
}

// nextSignificantLine возвращает индекс ближайшей строки начиная с from, которая не пустая и не комментарий
func nextSignificantLine(lines []string, from int) (int, bool) {
	for j := from; j < len(lines); j++ {
		line := strings.TrimSpace(lines[j])

		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		return j, true
	}

	return 0, false
}

// openBrace возвращает индекс первой строки тела блока. Открывающая '{' стоит либо в конце строки
// заголовка lines[headerIdx], либо одна на ближайшей следующей строке. keyword — for, if или else (для ошибки).
func openBrace(lines []string, headerIdx int, keyword string) (int, error) {
	if strings.HasSuffix(strings.TrimSpace(lines[headerIdx]), "{") {
		return headerIdx + 1, nil
	}

	if j, ok := nextSignificantLine(lines, headerIdx+1); ok && strings.TrimSpace(lines[j]) == "{" {
		return j + 1, nil
	}

	return 0, fmt.Errorf("строка %d: после %s ожидается '{' — в конце этой строки или на следующей", headerIdx+1, keyword)
}

// errNoOperator — в строке нет оператора сравнения (wait_event по нему отличает таймаут от условия)
var errNoOperator = errors.New("в условии нет оператора сравнения (== != < > <= >=)")

// parseWaitEvent разбирает wait_event(Имя, условие, ..., таймаут); expr — вся конструкция, начиная с wait_event
func (parser *Parser) parseWaitEvent(expr string, lineNum int) (Command, error) {
	syntax := fmt.Errorf("строка %d: неверный синтаксис wait_event, ожидается: wait_event(ИмяСобытия, поле == значение, секунды)", lineNum)

	m := waitEventRe.FindStringSubmatch(expr)
	if m == nil {
		return Command{}, syntax
	}

	args := splitArgs(m[1])
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}

	if len(args) == 0 || args[0] == "" {
		return Command{}, syntax
	}

	name := strings.Trim(args[0], `"`)
	if !eventNameRe.MatchString(name) {
		return Command{}, fmt.Errorf("строка %d: wait_event: неверное имя события %q", lineNum, args[0])
	}

	if _, ok := parser.methods[name]; !ok {
		return Command{}, fmt.Errorf("строка %d: wait_event: событие '%s' не найдено среди методов proto", lineNum, name)
	}

	cmd := Command{IsWaitEvent: true, EventName: name}

	for _, arg := range args[1:] {
		if arg == "" {
			return Command{}, fmt.Errorf("строка %d: wait_event: пустой аргумент", lineNum)
		}

		left, op, right, err := parseCondition(arg)

		switch {
		case err == nil:
			if !eventFieldRe.MatchString(left) {
				return Command{}, fmt.Errorf("строка %d: wait_event: слева от %s должно быть имя поля события, получено %q", lineNum, op, left)
			}

			cmd.EventConds = append(cmd.EventConds, EventCondition{Field: left, Op: op, Value: right})
		case errors.Is(err, errNoOperator):
			// аргумент без оператора — таймаут в секундах
			if cmd.EventTimeout != "" {
				return Command{}, fmt.Errorf("строка %d: wait_event: лишний аргумент %q: таймаут уже задан (%s)", lineNum, arg, cmd.EventTimeout)
			}

			cmd.EventTimeout = arg
		default:
			return Command{}, fmt.Errorf("строка %d: wait_event: %v", lineNum, err)
		}
	}

	return cmd, nil
}

// parseCondition разбирает условие "левый оператор правый" на части.
// Операторы: == != < > <= >=. Операнды — любые выражения сценария; строки в кавычках могут содержать любые символы.
func parseCondition(cond string) (left, op, right string, err error) {
	inQuotes := false
	opStart, opLen := -1, 0

	for i := 0; i < len(cond); i++ {
		ch := cond[i]

		if ch == '"' {
			inQuotes = !inQuotes
			continue
		}

		if inQuotes {
			continue
		}

		if i+1 < len(cond) && ((ch == '&' && cond[i+1] == '&') || (ch == '|' && cond[i+1] == '|')) {
			return "", "", "", fmt.Errorf("в условии '%s' не поддерживается: допустимо одно сравнение", cond[i:i+2])
		}

		if ch != '=' && ch != '!' && ch != '<' && ch != '>' {
			continue
		}

		length := 1
		if i+1 < len(cond) && cond[i+1] == '=' {
			length = 2
		}

		found := cond[i : i+length]
		if found == "=" || found == "!" {
			return "", "", "", fmt.Errorf("неизвестный оператор '%s' в условии, допустимы: == != < > <= >=", found)
		}

		if opStart != -1 {
			return "", "", "", fmt.Errorf("в условии должен быть один оператор сравнения")
		}

		opStart, opLen = i, length
		i += length - 1
	}

	if opStart == -1 {
		return "", "", "", fmt.Errorf("%w: %s", errNoOperator, cond)
	}

	left = strings.TrimSpace(cond[:opStart])
	op = cond[opStart : opStart+opLen]
	right = strings.TrimSpace(cond[opStart+opLen:])

	if left == "" || right == "" {
		return "", "", "", fmt.Errorf("в условии не хватает значения слева или справа от '%s'", op)
	}

	return left, op, right, nil
}

// startsWithKeyword проверяет, что строка начинается с ключевого слова целиком:
// "sleep(1)" и "sleep 1" — да, "sleep_ms = 1" и "log = 1" — нет (это присваивание переменной)
func startsWithKeyword(line, keyword string) bool {
	if !strings.HasPrefix(line, keyword) {
		return false
	}

	rest := line[len(keyword):]

	if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '(' {
		return false
	}

	// "log = 1" — присваивание переменной с именем log, а "if a == b" — условие
	trimmed := strings.TrimSpace(rest)
	if strings.HasPrefix(trimmed, "=") && !strings.HasPrefix(trimmed, "==") {
		return false
	}

	return true
}

// UsesConnect сообщает, есть ли в сценарии команда connect() (на любой глубине вложенности)
func (scenario *Scenario) UsesConnect() bool {
	return anyConnect(scenario.Commands)
}

func anyConnect(commands []Command) bool {
	for _, cmd := range commands {
		if cmd.IsConnect || anyConnect(cmd.Body) || anyConnect(cmd.ElseBody) {
			return true
		}
	}

	return false
}

// parseLine разбирает одну строку: sleep, log, connect, присваивание или вызов метода.
// Второе значение false означает, что строку нужно пропустить (как и раньше, для нераспознанных строк).
func (parser *Parser) parseLine(line string, lineNum int) (Command, bool, error) {
	cmd := Command{
		RawLine: line,
	}

	// Пауза
	if startsWithKeyword(line, "sleep") {
		m := sleepRe.FindStringSubmatch(line)
		if m == nil || strings.TrimSpace(m[1]) == "" {
			return Command{}, false, fmt.Errorf("строка %d: неверный синтаксис sleep, ожидается: sleep(секунды)", lineNum)
		}

		cmd.IsSleep = true
		cmd.SleepArg = strings.TrimSpace(m[1])

		return cmd, true, nil
	}

	// Ожидание события без сохранения результата
	if startsWithKeyword(line, "wait_event") {
		waitCmd, err := parser.parseWaitEvent(line, lineNum)
		if err != nil {
			return Command{}, false, err
		}

		waitCmd.RawLine = line

		return waitCmd, true, nil
	}

	// RTP без сохранения результата
	if name := rtpKeyword(line); name != "" {
		rtpCmd, err := parser.parseRTP(line, name, lineNum)
		if err != nil {
			return Command{}, false, err
		}

		if rtpCmd.RTPOp == "rtp_open" {
			return Command{}, false, fmt.Errorf("строка %d: результат rtp_open нужно сохранить в переменную: rtp = rtp_open()", lineNum)
		}

		rtpCmd.RawLine = line

		return rtpCmd, true, nil
	}

	// Подключение к MM
	if startsWithKeyword(line, "connect") {
		if !connectRe.MatchString(line) {
			return Command{}, false, fmt.Errorf("строка %d: неверный синтаксис connect, ожидается: connect()", lineNum)
		}

		cmd.IsConnect = true

		return cmd, true, nil
	}

	// Вывод в лог и явный провал: log(...) и fail(...) записываются одинаково
	for _, word := range []string{"log", "fail"} {
		if !startsWithKeyword(line, word) {
			continue
		}

		m := logLikeRe[word].FindStringSubmatch(line)
		if m == nil {
			return Command{}, false, fmt.Errorf("строка %d: неверный синтаксис %s, ожидается: %s(\"текст\", переменная)", lineNum, word, word)
		}

		args := splitArgs(m[1])
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
			if args[i] == "" {
				return Command{}, false, fmt.Errorf("строка %d: %s: пустой аргумент", lineNum, word)
			}
		}

		if len(args) == 0 {
			return Command{}, false, fmt.Errorf("строка %d: %s без аргументов, ожидается: %s(\"текст\", переменная)", lineNum, word, word)
		}

		cmd.IsLog = word == "log"
		cmd.IsFail = word == "fail"
		cmd.LogArgs = args

		return cmd, true, nil
	}

	// Ищем присваивание с умной проверкой
	eqIdx, isAssignment := findAssignment(line)

	if isAssignment {
		left := strings.TrimSpace(line[:eqIdx])
		right := strings.TrimSpace(line[eqIdx+1:])

		cmd.VarName = left
		cmd.RightPart = right

		// resp.field = значение: в поле можно записать только значение, а не результат команды
		if strings.Contains(left, ".") && (startsWithKeyword(right, "wait_event") || rtpKeyword(right) != "" ||
			(strings.Contains(right, "(") && strings.HasSuffix(right, ")"))) {
			return Command{}, false, fmt.Errorf("строка %d: в поле %s можно записать только значение (число, строку, переменную), а не результат команды", lineNum, left)
		}

		// ev = wait_event(...): результат ожидания сохраняется в переменную
		if startsWithKeyword(right, "wait_event") {
			waitCmd, err := parser.parseWaitEvent(right, lineNum)
			if err != nil {
				return Command{}, false, err
			}

			waitCmd.RawLine = line
			waitCmd.VarName = left
			waitCmd.RightPart = right

			return waitCmd, true, nil
		}

		// x = rtp_open(...), st = rtp_stop(...): результат сохраняется в переменную
		if name := rtpKeyword(right); name != "" {
			rtpCmd, err := parser.parseRTP(right, name, lineNum)
			if err != nil {
				return Command{}, false, err
			}

			rtpCmd.RawLine = line
			rtpCmd.VarName = left
			rtpCmd.RightPart = right

			if rtpCmd.RTPOp == "rtp_start" || rtpCmd.RTPOp == "rtp_dtmf" {
				return Command{}, false, fmt.Errorf("строка %d: %s не возвращает значение, присваивание не нужно", lineNum, rtpCmd.RTPOp)
			}

			return rtpCmd, true, nil
		}

		if strings.Contains(right, "(") && strings.HasSuffix(right, ")") {
			cmd.IsFunctionCall = true

			parsedCmd, err := parser.parseCommand(right)
			if err != nil {
				return Command{}, false, fmt.Errorf("строка %d: %v", lineNum, err)
			}

			cmd.Name = parsedCmd.Name
			cmd.Args = parsedCmd.Args
			cmd.Method = parsedCmd.Method
		} else {
			cmd.IsSimpleAssign = true
			cmd.Value = right
		}

		return cmd, true, nil
	}

	// Обычный вызов функции без присваивания
	if strings.Contains(line, "(") && strings.HasSuffix(line, ")") {
		parsedCmd, err := parser.parseCommand(line)
		if err != nil {
			return Command{}, false, fmt.Errorf("строка %d: %v", lineNum, err)
		}

		cmd.Name = parsedCmd.Name
		cmd.Args = parsedCmd.Args
		cmd.Method = parsedCmd.Method

		return cmd, true, nil
	}

	return Command{}, false, nil
}

// findAssignment ищет присваивание с проверкой, что слева от = — переменная
func findAssignment(line string) (int, bool) {
	depth := 0
	for i, ch := range line {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		case '=':
			if depth == 0 {
				// Проверяем, что слева от = — корректное имя переменной
				left := strings.TrimSpace(line[:i])
				if isValidVarPath(left) {
					return i, true
				}
				// Если слева не переменная — значит это не присваивание
				return -1, false
			}
		}
	}
	return -1, false
}

// isValidVarPath проверяет левую часть присваивания: имя переменной или переменная.поле.поле
func isValidVarPath(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if !isValidVarName(part) {
			return false
		}
	}

	return true
}

// isValidVarName проверяет, что строка — это корректное имя переменной
func isValidVarName(s string) bool {
	if s == "" {
		return false
	}
	// Имя переменной может содержать буквы, цифры, подчёркивания
	// и должно начинаться с буквы или подчёркивания
	for i, ch := range s {
		if i == 0 {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_') {
				return false
			}
		} else {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') || ch == '_') {
				return false
			}
		}
	}
	return true
}

// parseCommand разбирает одну команду
func (parser *Parser) parseCommand(line string) (Command, error) {
	openIdx := strings.Index(line, "(")
	name := strings.TrimSpace(line[:openIdx])

	methodInfo, exists := parser.methods[name]
	if !exists {
		return Command{}, fmt.Errorf("метод '%s' не найден", name)
	}

	closeIdx := strings.LastIndex(line, ")")
	if closeIdx == -1 {
		return Command{}, fmt.Errorf("нет закрывающей скобки")
	}

	argsStr := line[openIdx+1 : closeIdx]
	rawArgs := splitArgs(argsStr)

	fields := methodInfo.Request.GetFields()
	args := make([]Argument, 0, len(fields))

	for i, rawArg := range rawArgs {
		if i >= len(fields) {
			break
		}
		field := fields[i]
		args = append(args, Argument{
			Name:  field.GetName(),
			Value: strings.TrimSpace(rawArg),
		})
	}

	return Command{
		Name:   name,
		Args:   args,
		Method: methodInfo,
	}, nil
}

// splitArgs разбивает строку аргументов по запятой
func splitArgs(s string) []string {
	var result []string
	var current strings.Builder
	inQuotes := false

	for _, ch := range s {
		switch ch {
		case '"':
			inQuotes = !inQuotes
			current.WriteRune(ch)
		case ',':
			if inQuotes {
				current.WriteRune(ch)
			} else {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}
