package scenario

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
)

// Executor выполняет команды сценария
type Executor struct {
	conn    *grpc.ClientConn
	methods map[string]*desc.MethodDescriptor
}

func NewExecutor(conn *grpc.ClientConn, methods map[string]*desc.MethodDescriptor) *Executor {
	return &Executor{
		conn:    conn,
		methods: methods,
	}
}

// Execute выполняет все команды сценария
func (e *Executor) Execute(scenario *Scenario) error {
	for i, cmd := range scenario.Commands {
		log.Printf("[%d] Выполнение: %s", i+1, cmd.Name)

		methodDesc, ok := e.methods[cmd.Name]
		if !ok {
			log.Printf("  ❌ Метод %s не найден", cmd.Name)
			continue
		}

		if err := e.executeCommand(cmd, methodDesc); err != nil {
			return fmt.Errorf("ошибка выполнения %s: %v", cmd.Name, err)
		}
	}

	log.Printf("✅ Сценарий выполнен")
	return nil
}

func (e *Executor) executeCommand(cmd Command, methodDesc *desc.MethodDescriptor) error {
	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())

	// Заполняем аргументы по порядку
	fields := methodDesc.GetInputType().GetFields()
	for i, arg := range cmd.Args {
		if i >= len(fields) {
			break
		}
		field := fields[i]
		value := parseValue(arg)
		if err := reqMsg.TrySetFieldByName(field.GetName(), value); err != nil {
			log.Printf("  ⚠️ Поле %s не установлено: %v", field.GetName(), err)
		}
	}

	reqJSON, _ := reqMsg.MarshalJSON()
	log.Printf("  📤 Запрос: %s", string(reqJSON))

	respMsg := dynamic.NewMessage(methodDesc.GetOutputType())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullMethod := fmt.Sprintf("/%s/%s", methodDesc.GetService().GetFullyQualifiedName(), methodDesc.GetName())

	if err := e.conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
		return err
	}

	respJSON, _ := respMsg.MarshalJSON()
	log.Printf("  📥 Ответ: %s", string(respJSON))

	return nil
}

// parseValue преобразует строку в значение
func parseValue(s string) interface{} {
	s = strings.TrimSpace(s)

	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		return strings.Trim(s, "\"")
	}

	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}

	var i int64
	if _, err := fmt.Sscan(s, &i); err == nil {
		return i
	}

	return s
}
