package scenario

import (
	"fmt"
	"log"
	"strconv"

	"protoloader"

	"github.com/jhump/protoreflect/desc"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/descriptorpb"
)

type Executor struct {
	conn       *grpc.ClientConn
	methods    map[string]protoloader.MethodInfo
	sessionID  string
	enumMap    map[string]int32
	exprParser ExprParser
}

func NewExecutor(conn *grpc.ClientConn, methods map[string]protoloader.MethodInfo, sessionID string, enumMap map[string]int32) *Executor {
	return &Executor{
		conn:      conn,
		methods:   methods,
		sessionID: sessionID,
		enumMap:   enumMap,
	}
}

func (e *Executor) Execute(scenario *Scenario) error {
	for i, cmd := range scenario.Commands {
		// 1. Простое присваивание (без функции)
		if cmd.IsSimpleAssign {
			// Резолвим значение справа
			val, err := e.exprParser.ResolveValue(cmd.Value)
			if err != nil {
				log.Printf("[%d] Ошибка резолва значения: %v", i+1, err)
				continue
			}
			e.exprParser.vars.Set(cmd.VarName, val)
			log.Printf("[%d] %s = %s", i+1, cmd.VarName, val)
			continue
		}

		// 2. Вызов функции (с присваиванием или без)
		if cmd.Name != "" {
			log.Printf("[%d] Команда: %s", i+1, cmd.Name)

			methodInfo, ok := e.methods[cmd.Name]
			if !ok {
				log.Printf("Метод %s не найден", cmd.Name)
				continue
			}

			// Собираем запрос и отправляем...
			// (код отправки такой же как раньше)

			// Сохраняем результат, если есть переменная
			if cmd.VarName != "" && cmd.IsFunctionCall {
				e.exprParser.vars.Set(cmd.VarName, string(respJSON))
				log.Printf("Сохранён ответ в переменную '%s'", cmd.VarName)
			}

			continue
		}

		log.Printf("[%d] Неизвестная команда: %s", i+1, cmd.RawLine)
	}
	return nil
}

func convertStringToType(s string, fieldDesc *desc.FieldDescriptor, enumMap map[string]int32, sessionID string) (interface{}, error) {
	if s == "SESSION_ID" && sessionID != "" {
		s = sessionID
	}

	switch fieldDesc.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return s, nil

	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		return strconv.ParseBool(s)

	case descriptorpb.FieldDescriptorProto_TYPE_UINT64:
		return strconv.ParseUint(s, 10, 64)

	case descriptorpb.FieldDescriptorProto_TYPE_UINT32:
		v, err := strconv.ParseUint(s, 10, 32)
		return uint32(v), err

	case descriptorpb.FieldDescriptorProto_TYPE_INT64:
		return strconv.ParseInt(s, 10, 64)

	case descriptorpb.FieldDescriptorProto_TYPE_INT32:
		v, err := strconv.ParseInt(s, 10, 32)
		return int32(v), err

	case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		if v, err := strconv.ParseInt(s, 10, 32); err == nil {
			return int32(v), nil
		}
		if val, ok := enumMap[s]; ok {
			return val, nil
		}
		return nil, fmt.Errorf("неизвестное значение enum: %s", s)

	default:
		return nil, fmt.Errorf("неподдерживаемый тип: %s", fieldDesc.GetType())
	}
}
