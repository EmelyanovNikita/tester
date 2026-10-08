package scenario

import (
	"fmt"
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
}

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
// Parse разбирает текст сценария
func (parser *Parser) Parse(text string) (*Scenario, error) {
	lines := strings.Split(text, "\n")
	scenario := &Scenario{}

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		cmd := Command{
			RawLine: line,
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
					return nil, fmt.Errorf("строка %d: %v", lineNum+1, err)
				}
				cmd.Name = parsedCmd.Name
				cmd.Args = parsedCmd.Args
				cmd.Method = parsedCmd.Method
			} else {
				cmd.IsSimpleAssign = true
				cmd.Value = right
			}
		} else {
			// Обычный вызов функции без присваивания
			if strings.Contains(line, "(") && strings.HasSuffix(line, ")") {
				parsedCmd, err := parser.parseCommand(line)
				if err != nil {
					return nil, fmt.Errorf("строка %d: %v", lineNum+1, err)
				}
				cmd.Name = parsedCmd.Name
				cmd.Args = parsedCmd.Args
				cmd.Method = parsedCmd.Method
			} else {
				continue
			}
		}

		scenario.Commands = append(scenario.Commands, cmd)
	}

	return scenario, nil
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
