package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"

	"tester_mm/internal/mmlink"
	"tester_mm/internal/protoloader"

	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
)

// sswServer — gRPC-сервер, который тестер поднимает от имени SSW.
// MM вызывает на нём Connect и сообщает, куда и с каким session_id подключаться; остальное он только логирует.
type sswServer struct {
	link    *mmlink.Link
	methods map[string]protoloader.MethodInfo
}

func (server *sswServer) serve(address string, port int) {
	addr := fmt.Sprintf("%s:%d", address, port)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Ошибка запуска сервера: %v", err)
	}

	grpcServer := grpc.NewServer()

	if err := server.registerServices(grpcServer); err != nil {
		log.Fatalf("Ошибка регистрации сервисов: %v", err)
	}

	log.Printf("Сервер запущен на %s, ожидаем подключения...", addr)

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Ошибка Serve: %v", err)
	}
}

func (server *sswServer) registerServices(grpcServer *grpc.Server) error {
	// Группируем методы по сервисам
	servicesMap := make(map[string][]protoloader.MethodInfo)
	for _, methodInfo := range server.methods {
		svcName := methodInfo.Service
		servicesMap[svcName] = append(servicesMap[svcName], methodInfo)
	}

	for serviceName, methodsList := range servicesMap {
		serviceDesc := &grpc.ServiceDesc{
			ServiceName: serviceName,
			HandlerType: (*interface{})(nil),
			Methods:     []grpc.MethodDesc{},
			Streams:     []grpc.StreamDesc{},
		}

		for _, methodInfo := range methodsList {
			methodName := methodInfo.Method
			reqDesc := methodInfo.Request
			respDesc := methodInfo.Response
			svcName := serviceName

			handler := func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				reqMsg := dynamic.NewMessage(reqDesc)

				if err := dec(reqMsg); err != nil {
					return nil, err
				}

				reqJSON, _ := reqMsg.MarshalJSON()

				respData, err := server.handleRequest(svcName, methodName, reqJSON)
				if err != nil {
					log.Printf("Ошибка обработки: %v", err)
					return nil, err
				}

				respMsg := dynamic.NewMessage(respDesc)

				for k, v := range respData {
					if err := respMsg.TrySetFieldByName(k, v); err != nil {
						log.Printf("Ошибка установки поля %s: %v", k, err)
					}
				}

				return respMsg, nil
			}

			serviceDesc.Methods = append(serviceDesc.Methods, grpc.MethodDesc{
				MethodName: methodName,
				Handler:    handler,
			})
		}

		grpcServer.RegisterService(serviceDesc, nil)
	}

	return nil
}

func (server *sswServer) handleRequest(svcName, methodName string, reqJSON []byte) (map[string]interface{}, error) {
	switch methodName {
	case "Connect":
		return server.handleConnect(reqJSON)
	default:
		log.Printf("Получен запрос: %s.%s", svcName, methodName)
		log.Printf("Параметры: %s", string(reqJSON))
		return nil, nil
	}
}

// handleConnect принимает Connect от MM и передаёт параметры в mmlink: само подключение выполняется там
func (server *sswServer) handleConnect(reqJSON []byte) (map[string]interface{}, error) {
	log.Printf("=== Получен Connect запрос ===")
	log.Printf("Параметры: %s", string(reqJSON))

	var reqMap map[string]interface{}
	if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
		return nil, fmt.Errorf("парсинг Connect: %v", err)
	}

	var params mmlink.Params

	if v, ok := reqMap["sessionId"].(string); ok {
		params.SessionID = v
		log.Printf("Сохранён session_id: %s", params.SessionID)
	}

	if v, ok := reqMap["serverIp"].(string); ok {
		params.Address = v
		log.Printf("Сохранён IP: %s", params.Address)
	}

	if v, ok := reqMap["serverPort"].(float64); ok {
		params.Port = int(v)
		log.Printf("Сохранён порт: %d", params.Port)
	}

	if params.Address == "" || params.Port == 0 {
		return nil, fmt.Errorf("в Connect нет serverIp или serverPort: подключаться некуда")
	}

	if !server.link.OnConnect(params) {
		log.Printf("Повторный Connect проигнорирован: соединение устанавливается по первому")
	}

	return map[string]interface{}{
		"reply_code": int32(0),
	}, nil
}
