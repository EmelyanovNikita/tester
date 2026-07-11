package scenario

import (
	"fmt"
	"strings"

	"protoloader"
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

func (p *Parser) Parse(text string) (*Scenario, error) {
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

		// Проверяем, есть ли присваивание
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			left := strings.TrimSpace(parts[0])
			right := strings.TrimSpace(parts[1])

			cmd.VarName = left
			cmd.RightPart = right

			// Смотрим, что справа: функция или значение
			if strings.Contains(right, "(") && strings.HasSuffix(right, ")") {
				// Это вызов функции с присваиванием результата
				cmd.IsFunctionCall = true
				cmdLine := right

				// Парсим команду
				parsedCmd, err := p.parseCommand(cmdLine)
				if err != nil {
					return nil, fmt.Errorf("строка %d: %v", lineNum+1, err)
				}
				cmd.Name = parsedCmd.Name
				cmd.Args = parsedCmd.Args
				cmd.Method = parsedCmd.Method
			} else {
				// Это простое присваивание (без вызова функции)
				cmd.IsSimpleAssign = true
				cmd.Value = right
			}
		} else {
			// Обычный вызов функции без присваивания
			if strings.Contains(line, "(") && strings.HasSuffix(line, ")") {
				parsedCmd, err := p.parseCommand(line)
				if err != nil {
					return nil, fmt.Errorf("строка %d: %v", lineNum+1, err)
				}
				cmd.Name = parsedCmd.Name
				cmd.Args = parsedCmd.Args
				cmd.Method = parsedCmd.Method
			} else {
				// Непонятная строка — пропускаем
				continue
			}
		}

		scenario.Commands = append(scenario.Commands, cmd)
	}

	return scenario, nil
}

func (p *Parser) parseCommand(line string) (Command, error) {
	openIdx := strings.Index(line, "(")
	name := strings.TrimSpace(line[:openIdx])

	methodInfo, exists := p.methods[name]
	if !exists {
		return Command{}, fmt.Errorf("метод '%s' не найден", name)
	}

	closeIdx := strings.LastIndex(line, ")")
	if closeIdx == -1 {
		return Command{}, fmt.Errorf("нет закрывающей скобки")
	}

	argsStr := line[openIdx+1 : closeIdx]
	rawArgs := splitArgs(argsStr)

	// Получаем поля из MethodInfo
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
		Method: methodInfo, // ← сохраняем MethodInfo
	}, nil
}

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
