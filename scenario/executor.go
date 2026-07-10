package scenario

import (
	"log"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
)

type Executor struct {
	conn      *grpc.ClientConn
	methods   map[string]*desc.MethodDescriptor
	sessionID string // ← добавляем
}

func NewExecutor(conn *grpc.ClientConn, methods map[string]*desc.MethodDescriptor, sessionID string) *Executor {
	return &Executor{
		conn:      conn,
		methods:   methods,
		sessionID: sessionID,
	}
}

// convertStringToType преобразует строку в нужный тип
func convertStringToType(s string, fieldDesc protoreflect.FieldDescriptor, enumMap map[string]int32, sessionID string) (interface{}, error) {
	// Подстановка переменных
	if s == "SESSION_ID" && sessionID != "" {
		s = sessionID
	}

	switch fieldDesc.Kind() {
	case protoreflect.StringKind:
		return s, nil

	case protoreflect.BoolKind:
		return strconv.ParseBool(s)

	case protoreflect.Uint64Kind:
		return strconv.ParseUint(s, 10, 64)

	case protoreflect.Uint32Kind:
		v, err := strconv.ParseUint(s, 10, 32)
		return uint32(v), err

	case protoreflect.Int64Kind:
		return strconv.ParseInt(s, 10, 64)

	case protoreflect.Int32Kind:
		v, err := strconv.ParseInt(s, 10, 32)
		return int32(v), err

	case protoreflect.EnumKind:
		// Сначала пробуем как число
		if v, err := strconv.ParseInt(s, 10, 32); err == nil {
			return int32(v), nil
		}
		// Если не число, ищем по имени в enumMap
		if val, ok := enumMap[s]; ok {
			return val, nil
		}
		return nil, fmt.Errorf("неизвестное значение enum: %s", s)

	default:
		return nil, fmt.Errorf("неподдерживаемый тип: %s", fieldDesc.Kind().String())
	}
}

// Execute выполняет все команды сценария (только вывод и проверка)
func (e *Executor) Execute(scenario *Scenario) error {
	for i, cmd := range scenario.Commands {
		log.Printf("[%d] Команда: %s", i+1, cmd.Name)

		// Создаём сообщение запроса
		reqMsg := dynamic.NewMessage(cmd.Method.GetInputType())

		// Пробуем установить поля — ВСЕГДА ПЕРЕДАЁМ СТРОКИ!
		valid := true
		for _, arg := range cmd.Args {
			// Передаём значение как есть — строка!
			if err := reqMsg.TrySetFieldByName(arg.Name, arg.Value); err != nil {
				log.Printf("  ❌ Ошибка поля %s: %v", arg.Name, err)
				valid = false
			}
		}

		if !valid {
			log.Printf("  ❌ Команда %s содержит ошибки, пропускаем", cmd.Name)
			continue
		}

		// Выводим JSON запрос
		reqJSON, _ := reqMsg.MarshalJSON()
		log.Printf("  ✅ Запрос для %s:", cmd.Name)
		log.Printf("    %s", string(reqJSON))
	}

	return nil
}

// func (e *Executor) executeCommand(cmd Command, methodDesc *desc.MethodDescriptor) error {
// 	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())

// 	fields := methodDesc.GetInputType().GetFields()

// 	// Если команда ChannelAdd и есть аргумент session_id — подставляем
// 	sessionID := e.getSessionID()

// 	for i, arg := range cmd.Args {
// 		if i >= len(fields) {
// 			break
// 		}
// 		field := fields[i]

// 		// Подставляем session_id если аргумент "SESSION_ID"
// 		value := parseValue(arg)
// 		if field.GetName() == "session_id" && arg == "SESSION_ID" {
// 			value = sessionID
// 		}

// 		if err := reqMsg.TrySetFieldByName(field.GetName(), value); err != nil {
// 			log.Printf("  ⚠️ Поле %s не установлено: %v", field.GetName(), err)
// 		}
// 	}

// 	reqJSON, _ := reqMsg.MarshalJSON()
// 	log.Printf("  📤 Запрос: %s", string(reqJSON))

// 	respMsg := dynamic.NewMessage(methodDesc.GetOutputType())
// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()

// 	fullMethod := fmt.Sprintf("/%s/%s", methodDesc.GetService().GetFullyQualifiedName(), methodDesc.GetName())

// 	if err := e.conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
// 		return err
// 	}

// 	respJSON, _ := respMsg.MarshalJSON()
// 	log.Printf("  📥 Ответ: %s", string(respJSON))

// 	return nil
// }

// // getSessionID возвращает session_id из глобального клиента
// func (e *Executor) getSessionID() string {
// 	// Нужно передавать session_id через Executor
// 	return e.sessionID
// }
