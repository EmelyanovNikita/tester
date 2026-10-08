package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"tester_mm/internal/protoloader"

	"github.com/golang/protobuf/jsonpb"
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

	// ContinueOnError: при ошибке в команде не останавливать сценарий, а идти дальше
	ContinueOnError bool
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

// Execute выполняет команды сценария по порядку.
// По умолчанию останавливается на первой ошибке и возвращает её.
// При ContinueOnError ошибки только логируются, а в конце возвращается общая ошибка.
func (executor *Executor) Execute(scenario *Scenario) error {
	failed := 0

	for i, cmd := range scenario.Commands {
		if err := executor.runCommand(i+1, cmd); err != nil {
			failed++
			log.Printf("[%d] ОШИБКА: %v", i+1, err)

			if !executor.ContinueOnError {
				return fmt.Errorf("сценарий остановлен на команде %d: %w", i+1, err)
			}
		}
	}

	if failed > 0 {
		return fmt.Errorf("команд с ошибками: %d из %d", failed, len(scenario.Commands))
	}

	log.Printf("Сценарий выполнен, команд: %d", len(scenario.Commands))
	return nil
}

// runCommand выполняет одну команду сценария: присваивание или вызов метода
func (executor *Executor) runCommand(n int, cmd Command) error {
	// 1. Простое присваивание (без функции)
	if cmd.IsSimpleAssign {
		val, err := executor.exprParser.ResolveValue(cmd.Value)
		if err != nil {
			return fmt.Errorf("резолв значения для %s: %w", cmd.VarName, err)
		}

		executor.exprParser.vars.Set(cmd.VarName, val)
		log.Printf("[%d] %s = %s", n, cmd.VarName, val)

		return nil
	}

	// 2. Вызов функции
	if cmd.Name == "" {
		return fmt.Errorf("неизвестная команда: %s", cmd.RawLine)
	}

	log.Printf("[%d] Команда: %s", n, cmd.Name)

	methodInfo, ok := executor.methods[cmd.Name]
	if !ok {
		return fmt.Errorf("метод %s не найден", cmd.Name)
	}

	// Собираем запрос
	reqMsg := dynamic.NewMessage(methodInfo.Request)

	for _, arg := range cmd.Args {
		fieldDesc := methodInfo.Request.FindFieldByName(arg.Name)
		if fieldDesc == nil {
			return fmt.Errorf("поле %s не найдено в запросе %s", arg.Name, cmd.Name)
		}

		// Резолвим значение аргумента: то есть получаем из переменной/строки/цифры унифицированную строку
		resolved, err := executor.exprParser.ResolveValue(arg.Value)
		if err != nil {
			return fmt.Errorf("аргумент %s: %w", arg.Name, err)
		}

		// Из полученной строки необходимо получить конечное значение для заполнения запроса
		value, err := convertStringToType(resolved, fieldDesc, executor.enums, executor.sessionID)
		if err != nil {
			return fmt.Errorf("аргумент %s: %w", arg.Name, err)
		}

		// Пытаемся установить поле в запрос
		if err := reqMsg.TrySetFieldByName(arg.Name, value); err != nil {
			return fmt.Errorf("аргумент %s: %w", arg.Name, err)
		}
	}

	reqJSON, _ := reqMsg.MarshalJSON()
	log.Printf("Запрос для %s: %s", cmd.Name, string(reqJSON))

	// Создаем ответ, чтобы передать его при отправке запроса
	respMsg := dynamic.NewMessage(methodInfo.Response)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullMethod := fmt.Sprintf("/%s/%s", methodInfo.Service, cmd.Name)

	// Отправляем запрос и указываем, куда хотим получить ответ
	if err := executor.conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
		return fmt.Errorf("вызов %s: %w", cmd.Name, err)
	}

	respJSON, _ := respMsg.MarshalJSON()
	log.Printf("Ответ для %s: %s", cmd.Name, string(respJSON))

	// Сохраняем ответ, если в запросе было =.
	// В переменную кладём JSON вместе с нулевыми полями: иначе при успехе
	// (reply_code = RC_SUCCESS = 0) поле resp.replyCode в нём просто отсутствует.
	if cmd.VarName != "" && cmd.IsFunctionCall {
		fullJSON, err := respMsg.MarshalJSONPB(&jsonpb.Marshaler{EmitDefaults: true})
		if err != nil {
			return fmt.Errorf("сериализация ответа %s: %w", cmd.Name, err)
		}

		executor.exprParser.vars.Set(cmd.VarName, string(fullJSON))
		log.Printf("Сохранён ответ в переменную '%s'", cmd.VarName)
	}

	// Ответ с reply_code, отличным от RC_SUCCESS, считаем ошибкой команды
	if err := checkReplyCode(respMsg); err != nil {
		return fmt.Errorf("%s: %w", cmd.Name, err)
	}

	return nil
}

// checkReplyCode возвращает ошибку, если в ответе есть поле reply_code и оно не равно 0 (RC_SUCCESS)
func checkReplyCode(resp *dynamic.Message) error {
	fd := resp.GetMessageDescriptor().FindFieldByName("reply_code")
	if fd == nil || fd.GetType() != descriptorpb.FieldDescriptorProto_TYPE_ENUM {
		return nil
	}

	val, err := resp.TryGetField(fd)
	if err != nil {
		return fmt.Errorf("чтение reply_code: %w", err)
	}

	code, ok := val.(int32)
	if !ok {
		return fmt.Errorf("reply_code имеет неожиданный тип %T", val)
	}

	if code == 0 {
		return nil
	}

	name := fmt.Sprintf("%d", code)
	if ev := fd.GetEnumType().FindValueByNumber(code); ev != nil {
		name = ev.GetName()
	}

	return fmt.Errorf("MM вернул reply_code=%s", name)
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
		// Ключ может быть и именем поля из proto (on_ms), и JSON-именем (onMs):
		// именно так поля печатаются в логе запросов и ответов
		innerFieldDesc := msgType.FindFieldByName(k)
		if innerFieldDesc == nil {
			innerFieldDesc = msgType.FindFieldByJSONName(k)
		}

		// Неизвестный ключ раньше молча пропускался, и поле уходило на MM незаполненным
		if innerFieldDesc == nil {
			return nil, fmt.Errorf("неизвестное поле %q в сообщении %s", k, msgType.GetFullyQualifiedName())
		}

		// 5. Преобразуем значение
		val, err := convertValueByType(v, innerFieldDesc, enums, sessionID)
		if err != nil {
			return nil, fmt.Errorf("ошибка преобразования поля %s: %v", k, err)
		}

		// 6. Устанавливаем поле
		if err := msg.TrySetFieldByName(innerFieldDesc.GetName(), val); err != nil {
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
