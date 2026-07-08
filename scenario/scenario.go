package scenario

import (
	"fmt"
	"strings"
)

// Command представляет одну команду
type Command struct {
	Name string   // например "ChannelAdd"
	Args []string // аргументы в виде строк
}

// Scenario представляет сценарий
type Scenario struct {
	Commands []Command
}

// Parser парсит текст сценария
type Parser struct{}

func NewParser() *Parser {
	return &Parser{}
}

// Parse разбирает текст в команды
func (p *Parser) Parse(text string) (*Scenario, error) {
	lines := strings.Split(text, "\n")
	scenario := &Scenario{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.Contains(line, "(") && strings.HasSuffix(line, ")") {
			cmd, err := p.parseCommand(line)
			if err != nil {
				return nil, err
			}
			scenario.Commands = append(scenario.Commands, cmd)
		}
	}

	return scenario, nil
}

func (p *Parser) parseCommand(line string) (Command, error) {
	openIdx := strings.Index(line, "(")
	if openIdx == -1 {
		return Command{}, fmt.Errorf("нет открывающей скобки")
	}

	name := strings.TrimSpace(line[:openIdx])

	closeIdx := strings.LastIndex(line, ")")
	if closeIdx == -1 {
		return Command{}, fmt.Errorf("нет закрывающей скобки")
	}

	argsStr := line[openIdx+1 : closeIdx]
	var args []string

	if argsStr != "" {
		args = splitArgs(argsStr)
	}

	return Command{
		Name: name,
		Args: args,
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
				result = append(result, strings.TrimSpace(current.String()))
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		result = append(result, strings.TrimSpace(current.String()))
	}

	return result
}
