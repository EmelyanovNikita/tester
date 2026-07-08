package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
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
	RemoteServer GRPCServerConfig `json:"remote_server"`
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
				log.Printf("Получен запрос: %s.%s", svcName, methodName)
				log.Printf("Параметры: %s", string(reqJSON))

				respMsg := dynamic.NewMessage(respDesc)
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

func startClient(Address string, Port int) {
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
			defer conn.Close()
			select {}
		}

		log.Printf("Ошибка подключения: %v", err)
	}
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
	// printMethods(methods)

	go startServer(cfg.LocalServer.Address, cfg.LocalServer.Port, methods)

	startClient(cfg.RemoteServer.Address, cfg.RemoteServer.Port)

	select {}
}
