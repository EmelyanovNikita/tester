// scenario/vars.go
package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
)

// VarStorage хранилище переменных
type VarStorage struct {
	vars map[string]string // имя → значение (строка)
}

func NewVarStorage() *VarStorage {
	return &VarStorage{
		vars: make(map[string]string),
	}
}

// Set устанавливает переменную (просто строка)
func (storage *VarStorage) Set(name, value string) {
	storage.vars[name] = value
}

// Get возвращает значение переменной
func (storage *VarStorage) Get(name string) (string, bool) {
	val, ok := storage.vars[name]
	return val, ok
}

// SetField устанавливает поле объекта (переменная.поле = значение)
func (storage *VarStorage) SetField(varName, fieldPath string, value string) error {
	// Получаем JSON-строку объекта
	objJSON, ok := storage.vars[varName]
	if !ok {
		return fmt.Errorf("переменная '%storage' не найдена", varName)
	}

	// Парсим JSON
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(objJSON), &obj); err != nil {
		return fmt.Errorf("ошибка парсинга JSON для %storage: %v", varName, err)
	}

	// Разбираем путь: "codec.bitrate" → ["codec", "bitrate"]
	parts := strings.Split(fieldPath, ".")
	current := obj

	for i, part := range parts {
		if i == len(parts)-1 {
			// Последний элемент — устанавливаем значение
			current[part] = value
		} else {
			// Промежуточный элемент — переходим глубже
			next, ok := current[part].(map[string]interface{})
			if !ok {
				next = make(map[string]interface{})
				current[part] = next
			}
			current = next
		}
	}

	// Сериализуем обратно в JSON
	newJSON, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("ошибка сериализации JSON: %v", err)
	}

	storage.vars[varName] = string(newJSON)
	return nil
}

// GetField возвращает поле объекта (переменная.поле)
func (storage *VarStorage) GetField(varName, fieldPath string) (string, error) {
	// Получаем JSON-строку
	objJSON, ok := storage.vars[varName]
	if !ok {
		return "", fmt.Errorf("переменная '%storage' не найдена", varName)
	}

	// Парсим JSON
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(objJSON), &obj); err != nil {
		return "", fmt.Errorf("ошибка парсинга JSON для %storage: %v", varName, err)
	}

	// Разбираем путь
	parts := strings.Split(fieldPath, ".")
	current := obj

	for _, part := range parts {
		val, ok := current[part]
		if !ok {
			return "", fmt.Errorf("поле '%storage' не найдено", part)
		}
		// Если это вложенный объект — переходим
		if next, ok := val.(map[string]interface{}); ok {
			current = next
		} else {
			// Это конечное значение
			return fmt.Sprintf("%v", val), nil
		}
	}

	return "", fmt.Errorf("не удалось получить значение")
}
