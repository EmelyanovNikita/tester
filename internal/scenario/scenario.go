package scenario

import (
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
}

// Заголовок цикла: for i in 1..10 {   (границы — число или переменная, пробелы вокруг .. необязательны)
var forHeaderRe = regexp.MustCompile(`^for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(\S+?)\s*\.\.\s*(\S+?)\s*\{$`)

// Пауза: sleep(0.5)
var sleepRe = regexp.MustCompile(`^sleep\s*\((.*)\)$`)

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

	commands, _, err := parser.parseBlock(lines, 0, 0)
	if err != nil {
		return nil, err
	}

	return &Scenario{Commands: commands}, nil
}

// parseBlock разбирает строки начиная с индекса start.
// openLine == 0 — верхний уровень, читаем до конца текста.
// openLine > 0 — тело цикла for, начатого на этой строке (нумерация с 1), читаем до закрывающей "}".
// Возвращает команды и индекс строки, с которой нужно продолжать разбор.
func (parser *Parser) parseBlock(lines []string, start, openLine int) ([]Command, int, error) {
	var commands []Command

	for i := start; i < len(lines); i++ {
		lineNum := i + 1
		line := strings.TrimSpace(lines[i])

		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		// Конец тела цикла
		if line == "}" {
			if openLine == 0 {
				return nil, 0, fmt.Errorf("строка %d: лишняя '}' без открывающего for", lineNum)
			}

			return commands, i + 1, nil
		}

		// Начало цикла
		if startsWithKeyword(line, "for") {
			m := forHeaderRe.FindStringSubmatch(line)
			if m == nil {
				return nil, 0, fmt.Errorf("строка %d: неверный синтаксис for, ожидается: for i in 1..10 {", lineNum)
			}

			body, next, err := parser.parseBlock(lines, i+1, lineNum)
			if err != nil {
				return nil, 0, err
			}

			commands = append(commands, Command{
				RawLine:  line,
				IsFor:    true,
				LoopVar:  m[1],
				LoopFrom: m[2],
				LoopTo:   m[3],
				Body:     body,
			})

			// next — строка после закрывающей "}"; после i++ в цикле продолжим именно с неё
			i = next - 1

			continue
		}

		cmd, ok, err := parser.parseLine(line, lineNum)
		if err != nil {
			return nil, 0, err
		}

		if ok {
			commands = append(commands, cmd)
		}
	}

	if openLine != 0 {
		return nil, 0, fmt.Errorf("строка %d: цикл for не закрыт, не хватает '}'", openLine)
	}

	return commands, len(lines), nil
}

// startsWithKeyword проверяет, что строка начинается с ключевого слова целиком:
// "sleep(1)" и "sleep 1" — да, "sleep_ms = 1" — нет
func startsWithKeyword(line, keyword string) bool {
	if !strings.HasPrefix(line, keyword) {
		return false
	}

	rest := line[len(keyword):]

	return rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '('
}

// parseLine разбирает одну строку: sleep, присваивание или вызов метода.
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

	// Ищем присваивание с умной проверкой
	eqIdx, isAssignment := findAssignment(line)

	if isAssignment {
		left := strings.TrimSpace(line[:eqIdx])
		right := strings.TrimSpace(line[eqIdx+1:])

		cmd.VarName = left
		cmd.RightPart = right

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
				if isValidVarName(left) {
					return i, true
				}
				// Если слева не переменная — значит это не присваивание
				return -1, false
			}
		}
	}
	return -1, false
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
