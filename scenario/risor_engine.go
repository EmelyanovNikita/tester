package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"protoloader"

	"github.com/risor-io/risor"
)

// CommandCallback - функция для выполнения команды
type CommandCallback func(cmdName string, args []string) (string, error)

// RisorEngine - движок сценариев на основе Risor
type RisorEngine struct {
	callback CommandCallback
}

// NewRisorEngine создаёт новый движок
func NewRisorEngine(callback CommandCallback) *RisorEngine {
	return &RisorEngine{callback: callback}
}

// Execute выполняет сценарий
func (e *RisorEngine) Execute(
	scriptText string,
	sessionID string,
	enums map[string]int32,
	methods map[string]protoloader.MethodInfo,
) error {
	// Создаём глобальные переменные
	globals := make(map[string]interface{})

	// 1. SESSION_ID
	globals["SESSION_ID"] = sessionID

	// 2. Enum как числа
	for name, val := range enums {
		globals[name] = val
	}

	// 3. Создаём одну функцию call() для всех методов
	globals["call"] = func(cmdName string, args ...interface{}) (interface{}, error) {
		// Преобразуем args в строки
		strArgs := make([]string, len(args))
		for i, arg := range args {
			strArgs[i] = fmt.Sprintf("%v", arg)
		}

		log.Printf("=== ВЫЗОВ МЕТОДА: %s ===", cmdName)
		log.Printf("Количество аргументов: %d", len(args))

		// Вызываем callback
		respJSON, err := e.callback(cmdName, strArgs)
		if err != nil {
			return nil, err
		}

		log.Printf("Ответ JSON: %s", respJSON)

		// Парсим JSON в map
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(respJSON), &result); err != nil {
			return nil, fmt.Errorf("ошибка парсинга JSON: %v", err)
		}
		return result, nil
	}

	// 4. Вспомогательные функции
	globals["log"] = func(msg string) {
		log.Printf("[SCENARIO] %s", msg)
	}

	globals["sleep"] = func(seconds int) {
		time.Sleep(time.Duration(seconds) * time.Second)
	}

	// 5. Выполняем сценарий
	ctx := context.Background()
	_, err := risor.Eval(ctx, scriptText, risor.WithGlobals(globals))
	if err != nil {
		return fmt.Errorf("ошибка выполнения сценария: %v", err)
	}
	return nil
}
