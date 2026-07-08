package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
	"github.com/jhump/protoreflect/dynamic"
)

func main() {
	address := flag.String("address", "127.0.0.1", "адрес сервера")
	port := flag.Int("port", 7000, "порт сервера")
	cmd := flag.String("cmd", "", "команда в формате MethodName(field1:value1,field2:value2,...)")
	flag.Parse()

	if *cmd == "" {
		log.Fatal("Укажите команду: -cmd MethodName(field1:value1,...)")
	}

	log.Printf("Подключение к %s:%d", *address, *port)

	methodName, params, err := parseCommand(*cmd)
	if err != nil {
		log.Fatalf("Ошибка парсинга команды: %v", err)
	}

	parser := protoparse.Parser{
		ImportPaths: []string{"proto"},
	}

	files, err := parser.ParseFiles("mm_server_api.proto", "mm_objects.proto")
	if err != nil {
		log.Fatalf("Ошибка загрузки proto: %v", err)
	}

	var methodDesc *desc.MethodDescriptor
	var serviceName string

	for _, fd := range files {
		for _, svc := range fd.GetServices() {
			if !strings.Contains(svc.GetFullyQualifiedName(), "server") {
				continue
			}
			for _, m := range svc.GetMethods() {
				if m.GetName() == methodName {
					methodDesc = m
					serviceName = svc.GetFullyQualifiedName()
					break
				}
			}
			if methodDesc != nil {
				break
			}
		}
		if methodDesc != nil {
			break
		}
	}

	if methodDesc == nil {
		log.Fatalf("Метод %s не найден в сервисах с 'server' в названии", methodName)
	}

	fullMethod := fmt.Sprintf("/%s/%s", serviceName, methodName)
	log.Printf("Найден метод: %s", fullMethod)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		fmt.Sprintf("%s:%d", *address, *port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)

	if err != nil {
		log.Fatalf("Ошибка подключения: %v", err)
	}
	defer conn.Close()

	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())

	for k, v := range params {
		if err := reqMsg.TrySetFieldByName(k, v); err != nil {
			log.Printf("Предупреждение: поле %s не установлено: %v", k, err)
		}
	}

	reqJSON, _ := reqMsg.MarshalJSON()
	log.Printf("Запрос: %s", string(reqJSON))

	respMsg := dynamic.NewMessage(methodDesc.GetOutputType())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Printf("Отправка запроса...")
	err = conn.Invoke(ctx, fullMethod, reqMsg, respMsg)
	if err != nil {
		log.Fatalf("Ошибка вызова: %v", err)
	}

	respJSON, _ := respMsg.MarshalJSON()
	log.Printf("Ответ: %s", string(respJSON))
}

func parseCommand(cmd string) (string, map[string]interface{}, error) {
	idx := strings.Index(cmd, "(")
	if idx == -1 {
		return cmd, nil, nil
	}

	methodName := cmd[:idx]

	endIdx := strings.LastIndex(cmd, ")")
	if endIdx == -1 {
		return "", nil, fmt.Errorf("нет закрывающей скобки")
	}

	paramsStr := cmd[idx+1 : endIdx]
	params := make(map[string]interface{})

	if paramsStr != "" {
		parts := strings.Split(paramsStr, ",")
		for _, part := range parts {
			kv := strings.SplitN(part, ":", 2)
			if len(kv) != 2 {
				return "", nil, fmt.Errorf("неверный формат: %s", part)
			}
			key := strings.TrimSpace(kv[0])
			value := strings.TrimSpace(kv[1])
			params[key] = parseValue(value)
		}
	}

	return methodName, params, nil
}

func parseValue(s string) interface{} {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		return strings.Trim(s, "\"")
	}
	// Пробуем как uint64 (для полей типа uint64)
	var ui uint64
	if _, err := fmt.Sscan(s, &ui); err == nil {
		return ui
	}
	// Пробуем как int64
	var i int64
	if _, err := fmt.Sscan(s, &i); err == nil {
		return i
	}
	return s
}
