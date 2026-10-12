package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"

	"tester_mm/internal/events"
	"tester_mm/internal/mmlink"
	"tester_mm/internal/protoloader"

	"github.com/golang/protobuf/jsonpb"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
)

// sswServer — gRPC-сервер, который тестер поднимает от имени SSW.
// MM вызывает на нём Connect и сообщает, куда и с каким session_id подключаться; остальные вызовы (события)
// он логирует и кладёт в events, откуда их забирает команда сценария wait_event.
type sswServer struct {
	link    *mmlink.Link
	events  *events.Bus
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

				// Для событий JSON с нулевыми полями: иначе поле со значением 0 (например playerId) в нём просто отсутствует
				eventJSON, err := reqMsg.MarshalJSONPB(&jsonpb.Marshaler{EmitDefaults: true})
				if err != nil {
					log.Printf("Ошибка сериализации запроса %s.%s: %v", svcName, methodName, err)
					eventJSON = reqJSON
				}

				respData, err := server.handleRequest(svcName, methodName, reqJSON, eventJSON)
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

// handleRequest обрабатывает вызов MM: Connect запускает подключение, всё остальное считается событием.
// reqJSON — параметры для лога, eventJSON — те же параметры вместе с нулевыми полями для wait_event.
func (server *sswServer) handleRequest(svcName, methodName string, reqJSON, eventJSON []byte) (map[string]interface{}, error) {
	switch methodName {
	case "Connect":
		return server.handleConnect(reqJSON)
	default:
		log.Printf("Получен запрос: %s.%s", svcName, methodName)
		log.Printf("Параметры: %s", string(reqJSON))

		server.events.Publish(methodName, string(eventJSON))

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
