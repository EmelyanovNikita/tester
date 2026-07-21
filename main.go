package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"executor"
	"protoloader"
	script "scenario"

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
	ProtoPath   string           `json:"proto_path"`
	LocalServer GRPCServerConfig `json:"local_server"`
}

var (
	protoPath    string
	scenarioText string
)

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

func getProtoFiles(protoPath string) ([]string, error) {
	entries, err := os.ReadDir(protoPath)
	if err != nil {
		return nil, fmt.Errorf("чтение папки %s: %v", protoPath, err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".proto") {
			files = append(files, entry.Name())
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("не найдено .proto файлов в %s", protoPath)
	}

	return files, nil
}

// ===== НОВЫЙ ОБРАБОТЧИК CONNECT =====
func createConnectHandler(sessionManager *executor.SessionManager, scriptEngine *script.Engine) func([]byte) (map[string]interface{}, error) {
	var connectionEstablished bool

	return func(reqJSON []byte) (map[string]interface{}, error) {
		log.Printf("=== Получен Connect запрос ===")
		log.Printf("Параметры: %s", string(reqJSON))

		var reqMap map[string]interface{}
		if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
			return nil, fmt.Errorf("парсинг Connect: %v", err)
		}

		sessionID, _ := reqMap["sessionId"].(string)
		serverIP, _ := reqMap["serverIp"].(string)
		serverPort, _ := reqMap["serverPort"].(float64)

		log.Printf("Сохранён session_id: %s", sessionID)
		log.Printf("Сохранён IP: %s", serverIP)
		log.Printf("Сохранён порт: %d", int(serverPort))

		if !connectionEstablished {
			connectionEstablished = true

			// Подключаемся к MM
			target := fmt.Sprintf("%s:%d", serverIP, int(serverPort))
			log.Printf("Подключение к MM: %s", target)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, err := grpc.DialContext(
				ctx,
				target,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithBlock(),
			)
			if err != nil {
				log.Printf("Ошибка подключения к MM: %v", err)
				return nil, err
			}

			// Регистрируем соединение в SessionManager
			sessionManager.RegisterConnection(sessionID, conn)

			// Кладём SESSION_ID в Script Engine
			scriptEngine.SetVariable("SESSION_ID", sessionID)

			// Отправляем CredentialsSend
			if err := sendCredentialsSend(conn, sessionID); err != nil {
				log.Printf("Ошибка CredentialsSend: %v", err)
				return nil, err
			}

			log.Printf("Аутентификация успешна")

			// Выполняем сценарий в отдельной горутине
			go func() {
				time.Sleep(3 * time.Second)
				if err := scriptEngine.Execute(scenarioText); err != nil {
					log.Printf("Ошибка выполнения сценария: %v", err)
				}
			}()
		}

		return map[string]interface{}{
			"reply_code": int32(0),
		}, nil
	}
}

func sendCredentialsSend(conn *grpc.ClientConn, sessionID string) error {
	// Загружаем proto
	parser := protoparse.Parser{
		ImportPaths: []string{protoPath},
	}
	files, err := parser.ParseFiles("mm_server_api.proto", "mm_objects.proto")
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

	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())
	reqMsg.TrySetFieldByName("session_id", sessionID)
	reqMsg.TrySetFieldByName("instance", "tester")

	respMsg := dynamic.NewMessage(methodDesc.GetOutputType())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullMethod := fmt.Sprintf("/%s/%s", serviceName, methodDesc.GetName())
	return conn.Invoke(ctx, fullMethod, reqMsg, respMsg)
}

// ===== ЗАПУСК СЕРВЕРА =====
func startServer(addr string, handler func([]byte) (map[string]interface{}, error), protoData *protoloader.ProtoData) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Ошибка запуска сервера: %v", err)
	}

	grpcServer := grpc.NewServer()

	// Регистрируем сервисы с обработчиком
	if err := registerServices(grpcServer, handler, protoData); err != nil {
		log.Fatalf("Ошибка регистрации сервисов: %v", err)
	}

	log.Printf("Сервер запущен на %s, ожидаем подключения...", addr)

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Ошибка Serve: %v", err)
	}
}

func registerServices(grpcServer *grpc.Server, handler func([]byte) (map[string]interface{}, error), protoData *protoloader.ProtoData) error {
	// Находим дескриптор для ConnectResponse
	var connectRequestDesc *desc.MessageDescriptor
	var connectResponseDesc *desc.MessageDescriptor

	for _, methodInfo := range protoData.Methods {
		if methodInfo.Method == "Connect" {
			connectRequestDesc = methodInfo.Request
			connectResponseDesc = methodInfo.Response
			break
		}
	}

	if connectResponseDesc == nil {
		return fmt.Errorf("дескриптор ConnectResponse не найден")
	}

	desc := &grpc.ServiceDesc{
		ServiceName: "mm.client_api.ConnectionService",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Connect",
				Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
					// Декодируем запрос
					reqMsg := dynamic.NewMessage(connectRequestDesc) // временно используем для декодирования
					if err := dec(reqMsg); err != nil {
						return nil, err
					}

					reqJSON, _ := reqMsg.MarshalJSON()
					respData, err := handler(reqJSON)
					if err != nil {
						return nil, err
					}

					// Формируем ответ
					respMsg := dynamic.NewMessage(connectResponseDesc)
					for k, v := range respData {
						if err := respMsg.TrySetFieldByName(k, v); err != nil {
							log.Printf("Ошибка установки поля %s: %v", k, err)
						}
					}
					return respMsg, nil
				},
			},
		},
		Streams: []grpc.StreamDesc{},
	}

	grpcServer.RegisterService(desc, nil)
	return nil
}

// ===== MAIN =====
func main() {
	path, err := getArgs()
	if err != nil {
		log.Fatalf("Ошибка: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфига: %v", err)
	}

	protoPath = cfg.ProtoPath

	log.Printf("Proto path: %s", cfg.ProtoPath)
	log.Printf("Local server: %s:%d", cfg.LocalServer.Address, cfg.LocalServer.Port)

	// 1. Загружаем proto
	protoFiles, err := getProtoFiles(cfg.ProtoPath)
	if err != nil {
		log.Fatalf("Ошибка поиска proto: %v", err)
	}

	protoData, err := protoloader.LoadProto(cfg.ProtoPath, protoFiles...)
	if err != nil {
		log.Fatalf("Ошибка загрузки proto: %v", err)
	}

	log.Printf("Загружено методов: %d", len(protoData.Methods))
	log.Printf("Загружено enum: %d", len(protoData.Enums))

	// 2. Читаем сценарий
	scenarioBytes, err := os.ReadFile("scenario.txt")
	if err != nil {
		log.Fatalf("Ошибка чтения сценария: %v", err)
	}
	scenarioText = string(scenarioBytes)

	// 3. Создаём SessionManager (хранит соединения)
	sessionManager := executor.NewSessionManager(protoData.Methods)

	// 4. Создаём callback для Script Engine
	callback := func(sessionID, command string, args []string) ([]byte, error) {
		return sessionManager.Execute(sessionID, command, args)
	}

	// 5. Создаём Script Engine
	scriptEngine := script.NewEngine(protoData.Enums, callback)

	// 6. Создаём обработчик Connect
	connectHandler := createConnectHandler(sessionManager, scriptEngine)

	// 7. Запускаем сервер
	addr := fmt.Sprintf("%s:%d", cfg.LocalServer.Address, cfg.LocalServer.Port)
	go startServer(addr, connectHandler, protoData)

	// 8. Ждём
	select {}
}
