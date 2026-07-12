package scenario

import (
	"context"
	"encoding/json"
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
	conn       *grpc.ClientConn
	methods    map[string]protoloader.MethodInfo
	sessionID  string
	enums      map[string]int32
	exprParser *ExprParser
}

func NewExecutor(conn *grpc.ClientConn, methods map[string]protoloader.MethodInfo, sessionID string, enums map[string]int32) *Executor {
	vars := NewVarStorage()
	vars.Set("SESSION_ID", sessionID)

	return &Executor{
		conn:       conn,
		methods:    methods,
		sessionID:  sessionID,
		enums:      enums,
		exprParser: NewExprParser(vars, enums),
	}
}

// ===== ОСНОВНАЯ ЛОГИКА ВЫПОЛНЕНИЯ =====

func (executor *Executor) Execute(scenario *Scenario) error {
	for i, cmd := range scenario.Commands {
		// 1. Простое присваивание (без функции)
		if cmd.IsSimpleAssign {
			val, err := executor.exprParser.ResolveValue(cmd.Value)
			if err != nil {
				log.Printf("[%d] Ошибка резолва значения: %v", i+1, err)
				continue
			}

			executor.exprParser.vars.Set(cmd.VarName, val)
			log.Printf("[%d] %s = %s", i+1, cmd.VarName, val)

			continue
		}

		// 2. Вызов функции
		if cmd.Name != "" {
			log.Printf("[%d] Команда: %s", i+1, cmd.Name)

			// Получение команды по имени
			methodInfo, ok := executor.methods[cmd.Name]
			if !ok {
				log.Printf("Метод %s не найден", cmd.Name)
				continue
			}

			// Собираем запрос
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

				// Резолвим значение аргумента: то есть получаем из переменной/строки/цифры унифицированную строку
				resolved, err := executor.exprParser.ResolveValue(arg.Value)
				if err != nil {
					log.Printf("Ошибка резолва аргумента %s: %v", arg.Name, err)
					valid = false
					continue
				}

				// Из полученной строки необходимо получить конечное значение для заполнения запроса
				value, err := convertStringToType(resolved, fieldDesc, executor.enums, executor.sessionID)
				if err != nil {
					log.Printf("Ошибка преобразования поля %s: %v", arg.Name, err)
					valid = false
					continue
				}

				// Пытаемся установить поле в запрос
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
			log.Printf("Запрос для %s: %s", cmd.Name, string(reqJSON))

			// Создаем ответ, чтобы передать его при отправке запроса
			respMsg := dynamic.NewMessage(methodInfo.Response)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			fullMethod := fmt.Sprintf("/%s/%s", methodInfo.Service, cmd.Name)

			// Отправляем запрос и указывам, куда хотим получить ответ
			if err := executor.conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
				log.Printf("Ошибка вызова %s: %v", cmd.Name, err)
				continue
			}

			// Парсим ответ
			respJSON, _ := respMsg.MarshalJSON()
			log.Printf("Ответ для %s: %s", cmd.Name, string(respJSON))

			// Сохраняем ответ, если в запросе было =
			if cmd.VarName != "" && cmd.IsFunctionCall {
				executor.exprParser.vars.Set(cmd.VarName, string(respJSON))
				log.Printf("Сохранён ответ в переменную '%s'", cmd.VarName)
			}

			continue
		}

		log.Printf("[%d] Неизвестная команда: %s", i+1, cmd.RawLine)
	}
	return nil
}

// ===== ФУНКЦИИ ПРЕОБРАЗОВАНИЯ =====

// convertStringToType преобразует строку в значение нужного типа
func convertStringToType(s string, fieldDesc *desc.FieldDescriptor, enums map[string]int32, sessionID string) (interface{}, error) {
	if s == "SESSION_ID" && sessionID != "" {
		s = sessionID
	}

	// Repeated поля
	if fieldDesc.IsRepeated() {
		return convertRepeatedField(s, fieldDesc, enums, sessionID)
	}

	// Вложенные сообщения
	if fieldDesc.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
		return convertMessageField(s, fieldDesc, enums, sessionID)
	}

	// Обычные поля
	return convertPrimitiveField(s, fieldDesc, enums)
}

// convertRepeatedField преобразует JSON-массив в массив динамических сообщений
func convertRepeatedField(s string, fieldDesc *desc.FieldDescriptor, enums map[string]int32, sessionID string) (interface{}, error) {
	var rawArr []interface{}

	// 1. Парсим из json
	if err := json.Unmarshal([]byte(s), &rawArr); err != nil {
		return nil, fmt.Errorf("ошибка парсинга массива: %v", err)
	}

	// 2. Получаем тип сообщения
	msgType := fieldDesc.GetMessageType()
	if msgType == nil {
		return nil, fmt.Errorf("repeated поле не является сообщением")
	}

	// 3. Создаёт пустой слайс для будущих сообщений
	result := make([]*dynamic.Message, 0, len(rawArr))

	// 4. Обрабатываем каждый элемент массива
	for _, item := range rawArr {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("ошибка сериализации элемента: %v", err)
		}

		// 5. Создаём сообщение для элемента
		msg := dynamic.NewMessage(msgType)
		if err := msg.UnmarshalJSON(itemJSON); err != nil {
			return nil, fmt.Errorf("ошибка парсинга элемента: %v", err)
		}

		// 6. Добавляем в результат
		result = append(result, msg)
	}

	return result, nil
}

// convertMessageField преобразует JSON-объект в динамическое сообщение
func convertMessageField(s string, fieldDesc *desc.FieldDescriptor, enums map[string]int32, sessionID string) (interface{}, error) {
	// 1. Парсим JSON в map
	var msgMap map[string]interface{}
	if err := json.Unmarshal([]byte(s), &msgMap); err != nil {
		return nil, fmt.Errorf("ошибка парсинга сообщения: %v", err)
	}

	// 2. Получаем тип сообщения
	msgType := fieldDesc.GetMessageType()
	if msgType == nil {
		return nil, fmt.Errorf("поле не является сообщением")
	}

	// 3. Создаём сообщение
	msg := dynamic.NewMessage(msgType)

	// 4. Заполняем поля
	for k, v := range msgMap {
		var innerFieldDesc *desc.FieldDescriptor
		for _, f := range msgType.GetFields() {
			if f.GetName() == k {
				innerFieldDesc = f
				break
			}
		}

		if innerFieldDesc == nil {
			continue
		}

		// 5. Преобразуем значение
		val, err := convertValueByType(v, innerFieldDesc, enums, sessionID)
		if err != nil {
			return nil, fmt.Errorf("ошибка преобразования поля %s: %v", k, err)
		}

		// 6. Устанавливаем поле
		if err := msg.TrySetFieldByName(k, val); err != nil {
			return nil, fmt.Errorf("ошибка установки поля %s: %v", k, err)
		}
	}

	return msg, nil
}

// convertValueByType преобразует значение в зависимости от его типа
func convertValueByType(value interface{}, fieldDesc *desc.FieldDescriptor, enums map[string]int32, sessionID string) (interface{}, error) {
	switch v := value.(type) {
	case string:
		return convertStringToType(v, fieldDesc, enums, sessionID)

	case float64:
		// Для enum-полей преобразуем число в int32
		if fieldDesc.GetType() == descriptorpb.FieldDescriptorProto_TYPE_ENUM {
			return int32(v), nil
		}
		return v, nil

	case bool:
		return v, nil

	default:
		return v, nil
	}
}

// convertPrimitiveField преобразует примитивные типы
func convertPrimitiveField(s string, fieldDesc *desc.FieldDescriptor, enums map[string]int32) (interface{}, error) {
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
		if v, err := strconv.ParseInt(s, 10, 32); err == nil {
			return int32(v), nil
		}
		if val, ok := enums[s]; ok {
			return val, nil
		}
		return nil, fmt.Errorf("неизвестное значение enum: %s", s)

	default:
		return nil, fmt.Errorf("неподдерживаемый тип: %s", fieldDesc.GetType())
	}
}
