// scenario/expr.go
package scenario

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExprParser парсит выражения
type ExprParser struct {
	vars  *VarStorage
	enums map[string]int32
}

func NewExprParser(vars *VarStorage, enums map[string]int32) *ExprParser {
	return &ExprParser{
		vars:  vars,
		enums: enums,
	}
}

// ResolveValue резолвит значение:
//   - "\"hello\"" → строка (без кавычек)
//   - "123" → число
//   - ENUM_NAME → enum значение
//   - var_name.field → поле объекта
//   - var_name → значение переменной
//
// не попали никуда → вернем ошибку
func (parser *ExprParser) ResolveValue(token string) (string, error) {
	token = strings.TrimSpace(token)

	// 1. Строка в кавычках
	if strings.HasPrefix(token, "\"") && strings.HasSuffix(token, "\"") {
		str := strings.Trim(token, "\"")
		// Преобразуем экранированные последовательности в реальные символы
		str = strings.ReplaceAll(str, "\\r", "\r")
		str = strings.ReplaceAll(str, "\\n", "\n")
		str = strings.ReplaceAll(str, "\\t", "\t")
		return str, nil
	}

	// 2. JSON-массив или объект (возвращаем как строку для дальнейшего парсинга)
	if strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]") {
		return token, nil
	}
	if strings.HasPrefix(token, "{") && strings.HasSuffix(token, "}") {
		return token, nil
	}

	// 3. Ключевые слова (true, false, null)
	if token == "true" {
		return "true", nil
	}
	if token == "false" {
		return "false", nil
	}
	if token == "null" || token == "NULL" {
		return "null", nil
	}

	// 4. Число
	if _, err := strconv.ParseFloat(token, 64); err == nil {
		return token, nil
	}

	// 5. Поле объекта (var.field.another.field)
	if strings.Contains(token, ".") {
		return parser.resolveObjectPath(token)
	}

	// 6. ENUM
	if val, ok := parser.enums[token]; ok {
		return fmt.Sprintf("%d", val), nil
	}

	// 7. Переменная
	if val, ok := parser.vars.Get(token); ok {
		return val, nil
	}

	// 8. Если ничего не подошло — возвращаем как есть
	return token, fmt.Errorf("непонятное rvalue значение: %s", token)
}

// resolveObjectPath резолвит путь вида var.field.another.field
func (parser *ExprParser) resolveObjectPath(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("неверный формат пути: %s", token)
	}

	varName := parts[0]
	fieldPath := parts[1:]

	// Получаем JSON-строку переменной
	objJSON, ok := parser.vars.Get(varName)
	if !ok {
		return "", fmt.Errorf("переменная '%s' не найдена", varName)
	}

	// Парсим JSON
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(objJSON), &obj); err != nil {
		return "", fmt.Errorf("ошибка парсинга JSON для %s: %v", varName, err)
	}

	// Рекурсивно обходим путь
	current := obj
	for i, part := range fieldPath {
		val, ok := current[part]
		if !ok {
			return "", fmt.Errorf("поле '%s' не найдено в пути %s", part, token)
		}

		// Если это последний элемент — возвращаем значение
		if i == len(fieldPath)-1 {
			return fmt.Sprintf("%v", val), nil
		}

		// Иначе переходим в объект
		next, ok := val.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("поле '%s' не является объектом", part)
		}

		current = next
	}

	return "", fmt.Errorf("не удалось получить значение из %s", token)
}

// ParseAssignment разбирает присваивание: a = 5  или  resp.status = "OK"
func (p *ExprParser) ParseAssignment(line string) error {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("неверный формат присваивания: %s", line)
	}

	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	// Резолвим правую часть
	value, err := p.ResolveValue(right)
	if err != nil {
		return err
	}

	// Проверяем, есть ли точка в левой части (поле объекта)
	if strings.Contains(left, ".") {
		parts := strings.SplitN(left, ".", 2)
		varName := parts[0]
		fieldPath := parts[1]
		return p.vars.SetField(varName, fieldPath, value)
	}

	// Обычная переменная
	p.vars.Set(left, value)
	return nil
}
