// executor/executor.go
package executor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"protoloader"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// SessionManager хранит соединения по session_id
type SessionManager struct {
	connections map[string]*grpc.ClientConn
	methods     map[string]protoloader.MethodInfo
}

func NewSessionManager(methods map[string]protoloader.MethodInfo) *SessionManager {
	return &SessionManager{
		connections: make(map[string]*grpc.ClientConn),
		methods:     methods,
	}
}

// RegisterConnection регистрирует соединение для session_id
func (s *SessionManager) RegisterConnection(sessionID string, conn *grpc.ClientConn) {
	s.connections[sessionID] = conn
}

// Execute выполняет команду по JSON-аргументам
// Execute выполняет команду
func (s *SessionManager) Execute(sessionID, cmdName string, args []string) ([]byte, error) {
	// 1. Находим соединение
	conn, ok := s.connections[sessionID]
	if !ok {
		return nil, fmt.Errorf("соединение для сессии %s не найдено", sessionID)
	}

	// 2. Находим метод
	methodInfo, ok := s.methods[cmdName]
	if !ok {
		return nil, fmt.Errorf("команда %s не найдена", cmdName)
	}

	// 3. Создаём запрос
	reqMsg := dynamic.NewMessage(methodInfo.Request)

	// 4. Сопоставляем аргументы с полями по порядку
	fields := methodInfo.Request.GetFields()

	// session_id всегда первый аргумент
	if len(args) > 0 {
		reqMsg.TrySetFieldByName("session_id", args[0])
	}

	// Остальные аргументы по порядку, начиная с индекса 1
	for i := 1; i < len(args) && i < len(fields); i++ {
		field := fields[i]
		value, err := convertStringToType(args[i], field)
		if err != nil {
			return nil, fmt.Errorf("ошибка преобразования поля %s: %v", field.GetName(), err)
		}
		if err := reqMsg.TrySetFieldByName(field.GetName(), value); err != nil {
			return nil, fmt.Errorf("ошибка установки поля %s: %v", field.GetName(), err)
		}
	}

	// 5. Отправляем
	respMsg := dynamic.NewMessage(methodInfo.Response)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullMethod := fmt.Sprintf("/%s/%s", methodInfo.Service, cmdName)
	if err := conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
		return nil, err
	}

	// 6. Возвращаем ответ как JSON
	return respMsg.MarshalJSON()
}

func convertStringToType(s string, fieldDesc *desc.FieldDescriptor) (interface{}, error) {
	switch fieldDesc.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return s, nil

	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		if s == "true" {
			return true, nil
		}
		if s == "false" {
			return false, nil
		}
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
		// ENUM уже зарезолвлен в Script Engine, приходит как число
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("неверное значение enum: %s", s)
		}
		return int32(v), nil

	default:
		return nil, fmt.Errorf("неподдерживаемый тип: %s", fieldDesc.GetType())
	}
}
