package script

import (
	"fmt"
	"strconv"
	"strings"
)

// CommandCallback вызывается при обнаружении команды в сценарии
type CommandCallback func(sessionID, command string, args []string) ([]byte, error)

// Engine обрабатывает сценарий
type Engine struct {
	enums     map[string]int32
	variables map[string]string
	commands  map[string]bool
	callback  CommandCallback
}

func NewEngine(enums map[string]int32, cb CommandCallback) *Engine {
	return &Engine{
		enums:     enums,
		variables: make(map[string]string),
		commands:  map[string]bool{},
		callback:  cb,
	}
}

// SetVariable устанавливает переменную
func (e *Engine) SetVariable(name, value string) {
	e.variables[name] = value
}

// Execute выполняет сценарий
func (e *Engine) Execute(scenario string) error {
	lines := strings.Split(scenario, "\n")

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.Contains(line, "=") {
			if err := e.handleAssignment(line); err != nil {
				return fmt.Errorf("строка %d: %v", lineNum+1, err)
			}
			continue
		}

		if e.isControlCommand(line) {
			if err := e.handleControlCommand(line); err != nil {
				return fmt.Errorf("строка %d: %v", lineNum+1, err)
			}
			continue
		}

		if strings.Contains(line, "(") && strings.HasSuffix(line, ")") {
			if err := e.handleCommand(line); err != nil {
				return fmt.Errorf("строка %d: %v", lineNum+1, err)
			}
			continue
		}

		return fmt.Errorf("строка %d: неизвестная конструкция: %s", lineNum+1, line)
	}
	return nil
}

// handleAssignment обрабатывает присваивание
func (e *Engine) handleAssignment(line string) error {
	parts := strings.SplitN(line, "=", 2)
	varName := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	if strings.Contains(right, "(") && strings.HasSuffix(right, ")") {
		return e.handleCommandWithResult(varName, right)
	}

	value, err := e.resolveValue(right)
	if err != nil {
		return err
	}
	e.variables[varName] = value
	return nil
}

// handleCommandWithResult обрабатывает команду и сохраняет результат
func (e *Engine) handleCommandWithResult(varName, cmdLine string) error {
	if err := e.handleCommand(cmdLine); err != nil {
		return err
	}
	if resp, ok := e.variables["resp"]; ok {
		e.variables[varName] = resp
	}
	return nil
}

// handleCommand обрабатывает вызов команды
func (e *Engine) handleCommand(line string) error {
	openIdx := strings.Index(line, "(")
	cmdName := strings.TrimSpace(line[:openIdx])

	argsStr := line[openIdx+1 : len(line)-1]
	rawArgs := splitArgs(argsStr)

	// Резолвим каждый аргумент в значение
	resolvedArgs := make([]string, 0, len(rawArgs))
	for _, arg := range rawArgs {
		resolved, err := e.resolveValue(arg)
		if err != nil {
			return err
		}
		resolvedArgs = append(resolvedArgs, resolved)
	}

	sessionID, ok := e.variables["SESSION_ID"]
	if !ok {
		return fmt.Errorf("SESSION_ID не установлен")
	}

	respJSON, err := e.callback(sessionID, cmdName, resolvedArgs)
	if err != nil {
		return err
	}

	e.variables["resp"] = string(respJSON)
	return nil
}

// resolveValue резолвит значение: переменная, enum, строка, число
func (e *Engine) resolveValue(token string) (string, error) {
	token = strings.TrimSpace(token)

	if strings.HasPrefix(token, "\"") && strings.HasSuffix(token, "\"") {
		str := strings.Trim(token, "\"")
		str = strings.ReplaceAll(str, "\\r", "\r")
		str = strings.ReplaceAll(str, "\\n", "\n")
		return str, nil
	}

	if _, err := strconv.ParseFloat(token, 64); err == nil {
		return token, nil
	}

	if token == "true" {
		return "true", nil
	}
	if token == "false" {
		return "false", nil
	}

	if val, ok := e.enums[token]; ok {
		return fmt.Sprintf("%d", val), nil
	}

	if val, ok := e.variables[token]; ok {
		return val, nil
	}

	return token, fmt.Errorf("неизвестное значение: %s", token)
}

// isControlCommand проверяет, является ли строка управляющей командой
func (e *Engine) isControlCommand(line string) bool {
	for cmd := range e.commands {
		if strings.HasPrefix(line, cmd+"(") && strings.HasSuffix(line, ")") {
			return true
		}
	}
	return false
}

// handleControlCommand обрабатывает управляющие команды
func (e *Engine) handleControlCommand(line string) error {
	return fmt.Errorf("управляющие команды пока не поддерживаются: %s", line)
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
