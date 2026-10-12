package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"tester_mm/internal/mmlink"
	"tester_mm/internal/protoloader"
	"tester_mm/internal/scenario"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
)

var (
	configPath      string
	scenarioPath    string
	continueOnError bool
	keepAlive       bool
)

func init() {
	pflag.StringVarP(&configPath, "path", "p", "", "путь к конфигурационному файлу")
	pflag.StringVarP(&scenarioPath, "scenario", "s", "scenario.txt", "путь к файлу сценария")
	pflag.BoolVar(&continueOnError, "continue-on-error", false, "не останавливать сценарий при ошибке в команде")
	pflag.BoolVar(&keepAlive, "keep-alive", false, "не завершать тестер после выполнения сценария")
}

type GRPCServerConfig struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type Config struct {
	ProtoRoot   string           `json:"proto_root"`
	ProtoFiles  []string         `json:"proto_files"`
	LocalServer GRPCServerConfig `json:"local_server"`

	// StartDelaySec — пауза в секундах между подключением к MM и началом сценария, у которого нет своего connect().
	// Нужна, чтобы MM успел подключить свои серверы. Если поле не задано, берётся defaultStartDelaySec.
	StartDelaySec *float64 `json:"start_delay_sec"`
}

// defaultStartDelaySec — прежнее поведение тестера: 3 секунды после аутентификации
const defaultStartDelaySec = 3

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение файла: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("парсинг JSON: %w", err)
	}

	return &cfg, nil
}

// startDelay возвращает паузу перед сценарием из конфига
func (cfg *Config) startDelay() (time.Duration, error) {
	seconds := float64(defaultStartDelaySec)
	if cfg.StartDelaySec != nil {
		seconds = *cfg.StartDelaySec
	}

	if seconds < 0 || seconds > 3600 {
		return 0, fmt.Errorf("start_delay_sec: ожидается число секунд от 0 до 3600, получено %v", seconds)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

// finish завершает процесс с кодом возврата; с --keep-alive тестер остаётся работать
func finish(code int) {
	if keepAlive {
		log.Printf("--keep-alive: тестер продолжает работать (код завершения был бы %d)", code)
		select {}
	}

	os.Exit(code)
}

// runScenario выполняет сценарий. Соединение с MM устанавливается через link:
//   - если в сценарии есть connect(), подключение происходит в том месте, где он стоит;
//   - если нет — тестер подключается сам перед сценарием и ждёт startDelay (как было до появления connect()).
func runScenario(link *mmlink.Link, scenarioObj *scenario.Scenario, data *protoloader.ProtoData, startDelay time.Duration) error {
	executor := scenario.NewExecutor(nil, data.Methods, "", data.Enums)
	executor.ContinueOnError = continueOnError

	executor.SetConnector(func() (*grpc.ClientConn, string, error) {
		session, err := link.Ensure(context.Background())
		if err != nil {
			return nil, "", err
		}

		return session.Conn, session.ID, nil
	})

	if !scenarioObj.UsesConnect() {
		log.Printf("В сценарии нет connect(): подключаемся к MM перед его началом")

		if _, err := executor.EnsureConnected(); err != nil {
			return err
		}

		if startDelay > 0 {
			log.Printf("Пауза перед сценарием: %v (start_delay_sec)", startDelay)
			time.Sleep(startDelay)
		}
	}

	return executor.Execute(scenarioObj)
}

func main() {
	pflag.Parse()

	if configPath == "" {
		log.Fatal("Укажите путь к конфигу: -p /path/to/config.json")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфига: %v", err)
	}

	if cfg.ProtoRoot == "" {
		log.Fatal("В конфиге не указан proto_root")
	}

	if len(cfg.ProtoFiles) == 0 {
		log.Fatal("В конфиге не указаны proto_files")
	}

	startDelay, err := cfg.startDelay()
	if err != nil {
		log.Fatalf("Ошибка конфига: %v", err)
	}

	log.Printf("Proto root: %s", cfg.ProtoRoot)
	log.Printf("Proto files: %v", cfg.ProtoFiles)
	log.Printf("Local server: %s:%d", cfg.LocalServer.Address, cfg.LocalServer.Port)

	// Загружаем proto по явным путям
	protoData, err := protoloader.LoadProto(cfg.ProtoRoot, cfg.ProtoFiles...)
	if err != nil {
		log.Fatalf("Ошибка загрузки proto: %v", err)
	}

	log.Printf("Загружено методов: %d", len(protoData.Methods))
	log.Printf("Загружено enum: %d", len(protoData.Enums))

	// Читаем сценарий
	scenarioText, err := os.ReadFile(scenarioPath)
	if err != nil {
		log.Fatalf("Ошибка чтения сценария: %v", err)
	}

	scenarioObj, err := scenario.NewParser(protoData.Methods).Parse(string(scenarioText))
	if err != nil {
		log.Fatalf("Ошибка парсинга сценария: %v", err)
	}

	log.Printf("Сценарий загружен, команд: %d", len(scenarioObj.Commands))

	// Логика подключения к MM целиком в mmlink; здесь только соединяем части
	link := mmlink.New(mmlink.NewEstablisher(protoData.Methods))

	server := &sswServer{link: link, methods: protoData.Methods}
	go server.serve(cfg.LocalServer.Address, cfg.LocalServer.Port)

	if err := runScenario(link, scenarioObj, protoData, startDelay); err != nil {
		log.Printf("Сценарий завершился с ошибкой: %v", err)
		finish(1)
	}

	log.Printf("Сценарий завершился успешно")
	finish(0)
}
