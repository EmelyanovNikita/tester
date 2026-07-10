package scenario

import (
	"fmt"
	"strings"

	"github.com/jhump/protoreflect/desc"
)

type Argument struct {
	Name  string // имя поля из proto
	Value string // значение в виде строки
}

type Command struct {
	Name   string                 // имя метода
	Args   []Argument             // список аргументов с именами полей
	Method *desc.MethodDescriptor // найденный метод
}

type Scenario struct {
	Commands []Command
}

// Parser парсит сценарий и сопоставляет с методами
type Parser struct {
	methodsMap map[string]*desc.MethodDescriptor
}

func NewParser(methodsMap map[string]*desc.MethodDescriptor) *Parser {
	return &Parser{
		methodsMap: methodsMap,
	}
}

// Parse разбирает текст в команды
func (p *Parser) Parse(text string) (*Scenario, error) {
	lines := strings.Split(text, "\n")
	scenario := &Scenario{}

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !strings.Contains(line, "(") || !strings.HasSuffix(line, ")") {
			continue
		}

		cmd, err := p.parseCommand(line)
		if err != nil {
			return nil, fmt.Errorf("строка %d: %v", lineNum+1, err)
		}
		scenario.Commands = append(scenario.Commands, cmd)
	}

	return scenario, nil
}

func (p *Parser) parseCommand(line string) (Command, error) {
	openIdx := strings.Index(line, "(")
	name := strings.TrimSpace(line[:openIdx])

	methodDesc, exists := p.methodsMap[name]
	if !exists {
		return Command{}, fmt.Errorf("метод '%s' не найден", name)
	}

	closeIdx := strings.LastIndex(line, ")")
	if closeIdx == -1 {
		return Command{}, fmt.Errorf("нет закрывающей скобки")
	}

	argsStr := line[openIdx+1 : closeIdx]
	rawArgs := splitArgs(argsStr)

	fields := methodDesc.GetInputType().GetFields()
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
		Method: methodDesc,
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
