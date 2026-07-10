package scenario

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"protoloader"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/descriptorpb"
)

type Executor struct {
	conn      *grpc.ClientConn
	methods   map[string]protoloader.MethodInfo
	sessionID string
	enumMap   map[string]int32
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
		log.Printf("[%d] Команда: %s", i+1, cmd.Name)

		methodInfo, ok := e.methods[cmd.Name]
		if !ok {
			log.Printf("Метод %s не найден", cmd.Name)
			continue
		}

		reqMsg := dynamic.NewMessage(methodInfo.Request)
		valid := true

		for _, arg := range cmd.Args {
			var fieldDesc *desc.FieldDescriptor
			for _, f := range methodInfo.Request.GetFields() {
				if f.GetName() == arg.Name {
					fieldDesc = f
					break
				}
			}

			if fieldDesc == nil {
				log.Printf("Поле %s не найдено", arg.Name)
				valid = false
				continue
			}

			value, err := convertStringToType(arg.Value, fieldDesc, e.enumMap, e.sessionID)
			if err != nil {
				log.Printf("Ошибка преобразования поля %s: %v", arg.Name, err)
				valid = false
				continue
			}

			if err := reqMsg.TrySetFieldByName(arg.Name, value); err != nil {
				log.Printf("Ошибка поля %s: %v", arg.Name, err)
				valid = false
			}
		}

		if !valid {
			log.Printf("Команда %s содержит ошибки, пропускаем", cmd.Name)
			continue
		}

		reqJSON, _ := reqMsg.MarshalJSON()
		log.Printf("Запрос для %s:", cmd.Name)
		log.Printf("    %s", string(reqJSON))

		// === ОТПРАВКА ===
		respMsg := dynamic.NewMessage(methodInfo.Response)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		fullMethod := fmt.Sprintf("/%s/%s", methodInfo.Service, cmd.Name)
		log.Printf("Отправка: %s", fullMethod)

		if err := e.conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
			cancel()
			return fmt.Errorf("ошибка вызова %s: %v", cmd.Name, err)
		}
		cancel()

		respJSON, _ := respMsg.MarshalJSON()
		log.Printf("Ответ для %s: %s", cmd.Name, string(respJSON))
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
