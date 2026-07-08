package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type GRPCServerConfig struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type Config struct {
	ProtoPath    string           `json:"proto_path"`
	LocalServer  GRPCServerConfig `json:"local_server"`
}

type MethodInfo struct {
	Service    string
	Method     string
	Request    *desc.MessageDescriptor
	Response   *desc.MessageDescriptor
	ReqFields  []FieldInfo
	RespFields []FieldInfo
}

type FieldInfo struct {
	Name string
	Type string
}

var (
	connMutex sync.Mutex
	mmClient  = &Client{}
)

type Client struct {
	SessionID string
	Address   string
	Port      int
	Conn      *grpc.ClientConn
	mu        sync.Mutex
}

func getArgs() (string, error) {
	path := pflag.StringP("path", "p", "", "path to config")
	pflag.Parse()

	if *path == "" {
		return "", fmt.Errorf("укажите путь: -p /path/to/config или --path /path/to/config")
	}

	return *path, nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение файла: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("парсинг JSON: %w", err)
	}

	if cfg.ProtoPath == "" {
		cfg.ProtoPath = "proto"
	}

	return &cfg, nil
}

func loadProtoMethods(protoPath string) ([]MethodInfo, error) {
	parser := protoparse.Parser{
		ImportPaths: []string{protoPath},
	}

	files, err := parser.ParseFiles(
		"mm_server_api.proto",
		"mm_client_api.proto",
		"mm_objects.proto",
		"mm_info.proto",
	)
	if err != nil {
		return nil, fmt.Errorf("загрузка proto: %v", err)
	}

	var methods []MethodInfo

	for _, fd := range files {
		for _, svc := range fd.GetServices() {
			serviceName := svc.GetFullyQualifiedName()

			for _, m := range svc.GetMethods() {
				reqType := m.GetInputType()
				respType := m.GetOutputType()

				var reqFields []FieldInfo
				for _, f := range reqType.GetFields() {
					reqFields = append(reqFields, FieldInfo{
						Name: f.GetName(),
						Type: f.GetType().String(),
					})
				}

				var respFields []FieldInfo
				for _, f := range respType.GetFields() {
					respFields = append(respFields, FieldInfo{
						Name: f.GetName(),
						Type: f.GetType().String(),
					})
				}

				methods = append(methods, MethodInfo{
					Service:    serviceName,
					Method:     m.GetName(),
					Request:    reqType,
					Response:   respType,
					ReqFields:  reqFields,
					RespFields: respFields,
				})
			}
		}
	}

	return methods, nil
}

func printMethods(methods []MethodInfo) {
	fmt.Println("\n=== НАЙДЕННЫЕ МЕТОДЫ ===\n")

	for _, m := range methods {
		fmt.Printf("Сервис: %s\n", m.Service)
		fmt.Printf("Метод: %s\n", m.Method)
		fmt.Printf("Запрос: %s\n", m.Request.GetFullyQualifiedName())
		fmt.Printf("Ответ: %s\n", m.Response.GetFullyQualifiedName())

		if len(m.ReqFields) > 0 {
			fmt.Println("Поля запроса:")
			for _, f := range m.ReqFields {
				fmt.Printf("  - %s: %s\n", f.Name, f.Type)
			}
		}

		if len(m.RespFields) > 0 {
			fmt.Println("Поля ответа:")
			for _, f := range m.RespFields {
				fmt.Printf("  - %s: %s\n", f.Name, f.Type)
			}
		}
		fmt.Println("---")
	}

	fmt.Printf("\nВсего методов: %d\n", len(methods))
}

func startClient(Address string, Port int) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:%d", Address, Port)

	for {
		log.Printf("Попытка подключения к %s...", target)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)

		conn, err := grpc.DialContext(
			ctx,
			target,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		cancel()

		if err == nil {
			log.Printf("Клиент успешно подключен к %s", target)
			return conn, nil
		}

		log.Printf("Ошибка подключения: %v", err)
	}
}

// startMMClient подключается к MM и отправляет CredentialsSend
func startMMClient(serverIP string, serverPort int, sessionID string) {
	conn, err := startClient(serverIP, serverPort)
	if err != nil {
		log.Printf("Ошибка подключения: %v", err)
		return
	}

	mmClient.mu.Lock()
	mmClient.SessionID = sessionID
	mmClient.Address = serverIP
	mmClient.Port = serverPort
	mmClient.Conn = conn
	mmClient.mu.Unlock()

	log.Printf("Подключение к MM установлено")

	if err := sendCredentialsSend(); err != nil {
		log.Printf("Ошибка CredentialsSend: %v", err)
		return
	}

	log.Printf("Аутентификация успешна")
}

// sendCredentialsSend отправляет запрос CredentialsSend
func sendCredentialsSend() error {
	parser := protoparse.Parser{
		ImportPaths: []string{"proto"},
	}

	files, err := parser.ParseFiles(
		"mm_server_api.proto",
		"mm_objects.proto",
	)
	if err != nil {
		return fmt.Errorf("загрузка proto: %v", err)
	}

	var methodDesc *desc.MethodDescriptor
	var serviceName string

	for _, fd := range files {
		for _, svc := range fd.GetServices() {
			if svc.GetFullyQualifiedName() == "mm.server_api.VerificationService" {
				serviceName = svc.GetFullyQualifiedName()
				for _, m := range svc.GetMethods() {
					if m.GetName() == "CredentialsSend" {
						methodDesc = m
						break
					}
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
		return fmt.Errorf("метод CredentialsSend не найден")
	}

	fullMethod := fmt.Sprintf("/%s/%s", serviceName, methodDesc.GetName())
	log.Printf("Отправка CredentialsSend: %s", fullMethod)

	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())
	reqMsg.TrySetFieldByName("session_id", mmClient.SessionID)
	reqMsg.TrySetFieldByName("instance", "tester")

	reqJSON, _ := reqMsg.MarshalJSON()
	log.Printf("CredentialsSend запрос: %s", string(reqJSON))

	respMsg := dynamic.NewMessage(methodDesc.GetOutputType())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mmClient.mu.Lock()
	conn := mmClient.Conn
	mmClient.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("нет соединения с MM")
	}

	if err := conn.Invoke(ctx, fullMethod, reqMsg, respMsg); err != nil {
		return fmt.Errorf("ошибка вызова CredentialsSend: %v", err)
	}

	respJSON, _ := respMsg.MarshalJSON()
	log.Printf("CredentialsSend ответ: %s", string(respJSON))

	return nil
}

func handleConnect(reqJSON []byte) (map[string]interface{}, error) {
	log.Printf("=== Получен Connect запрос ===")
	log.Printf("Параметры: %s", string(reqJSON))

	var reqMap map[string]interface{}
	if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
		return nil, fmt.Errorf("парсинг Connect: %v", err)
	}

	var sessionID string
	var serverIP string
	var serverPort int

	if v, ok := reqMap["sessionId"].(string); ok {
		sessionID = v
		log.Printf("Сохранён session_id: %s", sessionID)
	}
	if v, ok := reqMap["serverIp"].(string); ok {
		serverIP = v
		log.Printf("Сохранён IP: %s", serverIP)
	}
	if v, ok := reqMap["serverPort"].(float64); ok {
		serverPort = int(v)
		log.Printf("Сохранён порт: %d", serverPort)
	}

	// Запускаем клиент в отдельной горутине
	go startMMClient(serverIP, serverPort, sessionID)

	return map[string]interface{}{
		"reply_code": int32(0),
	}, nil
}

func handleRequest(svcName, methodName string, reqJSON []byte) (map[string]interface{}, error) {
	switch methodName {
	case "Connect":
		return handleConnect(reqJSON)
	default:
		log.Printf("Получен запрос: %s.%s", svcName, methodName)
		log.Printf("Параметры: %s", string(reqJSON))
		return nil, nil
	}
}

func startServer(Address string, Port int, methods []MethodInfo) {
	addr := fmt.Sprintf("%s:%d", Address, Port)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Ошибка запуска сервера: %v", err)
	}

	grpcServer := grpc.NewServer()

	if err := registerServices(grpcServer, methods); err != nil {
		log.Fatalf("Ошибка регистрации сервисов: %v", err)
	}

	log.Printf("Сервер запущен на %s, ожидаем подключения...", addr)

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Ошибка Serve: %v", err)
	}
}

func registerServices(grpcServer *grpc.Server, methods []MethodInfo) error {
	servicesMap := make(map[string][]MethodInfo)
	for _, m := range methods {
		servicesMap[m.Service] = append(servicesMap[m.Service], m)
	}

	for serviceName, methodsList := range servicesMap {
		desc := &grpc.ServiceDesc{
			ServiceName: serviceName,
			HandlerType: (*interface{})(nil),
			Methods:     []grpc.MethodDesc{},
			Streams:     []grpc.StreamDesc{},
		}

		for _, m := range methodsList {
			methodName := m.Method
			reqDesc := m.Request
			respDesc := m.Response
			svcName := serviceName

			handler := func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				reqMsg := dynamic.NewMessage(reqDesc)

				if err := dec(reqMsg); err != nil {
					return nil, err
				}

				reqJSON, _ := reqMsg.MarshalJSON()

				respData, err := handleRequest(svcName, methodName, reqJSON)
				if err != nil {
					log.Printf("Ошибка обработки: %v", err)
					return nil, err
				}

				respMsg := dynamic.NewMessage(respDesc)

				if respData != nil {
					for k, v := range respData {
						if err := respMsg.TrySetFieldByName(k, v); err != nil {
							log.Printf("Ошибка установки поля %s: %v", k, err)
						}
					}
				}

				return respMsg, nil
			}

			desc.Methods = append(desc.Methods, grpc.MethodDesc{
				MethodName: methodName,
				Handler:    handler,
			})
		}

		grpcServer.RegisterService(desc, nil)
	}

	return nil
}

func main() {
	path, err := getArgs()
	if err != nil {
		log.Fatalf("Ошибка: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфига: %v", err)
	}

	log.Printf("Proto path: %s", cfg.ProtoPath)
	log.Printf("Remote server: %s:%d", cfg.RemoteServer.Address, cfg.RemoteServer.Port)
	log.Printf("Local server: %s:%d", cfg.LocalServer.Address, cfg.LocalServer.Port)

	methods, err := loadProtoMethods(cfg.ProtoPath)
	if err != nil {
		log.Fatalf("Ошибка загрузки proto: %v", err)
	}

	go startServer(cfg.LocalServer.Address, cfg.LocalServer.Port, methods)

	select {}
}
