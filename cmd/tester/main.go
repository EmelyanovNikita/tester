package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"tester_mm/internal/cmdstats"
	"tester_mm/internal/events"
	"tester_mm/internal/load"
	"tester_mm/internal/mmlink"
	"tester_mm/internal/protoloader"
	"tester_mm/internal/rtp"
	"tester_mm/internal/scenario"
	"tester_mm/internal/screen"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
)

var (
	configPath      string
	scenarioPaths   []string
	continueOnError bool
	keepAlive       bool

	// Нагрузочный режим: сценарий выполняют много клиентов одновременно
	callsPerSecond float64
	parallelCalls  int
	totalCalls     int
	pauseMillis    int
	verbose        bool
	screenMode     bool
)

func init() {
	pflag.StringVarP(&configPath, "path", "p", "", "путь к конфигурационному файлу")
	pflag.StringSliceVarP(&scenarioPaths, "scenario", "s", nil,
		"сценарий: файл или папка с файлами "+scenarioExt+"; можно несколько через запятую, повторяя флаг или просто перечисляя после него (по умолчанию — поле scenarios из конфига, иначе "+defaultScenario+")")
	pflag.BoolVar(&continueOnError, "continue-on-error", false, "не останавливать сценарий при ошибке в команде")
	pflag.Float64Var(&callsPerSecond, "cps", 0, "нагрузка: cps (calls per second) — сколько новых звонков начинать в секунду, ровно по расписанию; можно дробное (по умолчанию 10)")
	pflag.IntVar(&parallelCalls, "parallel", 0, "нагрузка: сколько звонков может идти одновременно (потолок; по умолчанию без ограничения)")
	pflag.IntVar(&totalCalls, "calls", 0, "нагрузка: сколько звонков сделать за весь тест (по умолчанию без ограничения, до Ctrl+C)")
	pflag.IntVar(&pauseMillis, "duration", 0, "длительность pause() без аргумента, мс (как -d в sipp)")
	pflag.BoolVar(&screenMode, "screen", false, "нагрузка с экраном в терминале, как в sipp: статистика сверху, таблица команд сценария ниже; клавиши + - * / меняют cps, p — пауза, q — завершить, Q — выйти сразу (лог при этом пишется в файл "+screenLogFile+")")
	pflag.BoolVar(&verbose, "verbose", false, "в нагрузочном режиме выводить подробный лог каждого клиента (по умолчанию только log(...) и ошибки)")
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

	// Scenarios — сценарии для запуска: файлы или папки (из папки берутся файлы .txt по алфавиту).
	// Используется, если сценарии не заданы флагом -s. Относительные пути отсчитываются от текущей папки.
	Scenarios []string `json:"scenarios"`

	// StartDelaySec — пауза в секундах между подключением к MM и началом сценария, у которого нет своего connect().
	// Нужна, чтобы MM успел подключить свои серверы. Если поле не задано, берётся defaultStartDelaySec.
	StartDelaySec *float64 `json:"start_delay_sec"`

	// Load — настройки нагрузки по умолчанию; флаги --cps, --parallel, --calls их переопределяют
	Load LoadConfig `json:"load"`

	// RTP — настройки медиа-потоков (rtp_open, rtp_start). Можно не задавать: используются значения по умолчанию.
	RTP RTPConfig `json:"rtp"`
}

// LoadConfig — параметры нагрузочного режима (то же, что флаги --cps, --parallel, --calls)
type LoadConfig struct {
	CPS      float64 `json:"cps"`
	Parallel int     `json:"parallel"`
	Calls    int     `json:"calls"`
}

// loadOptions собирает параметры нагрузки: значение из конфига, поверх него флаг, если он указан явно
func (cfg *Config) loadOptions(flagChanged func(name string) bool) load.Options {
	opts := load.Options{CPS: cfg.Load.CPS, Parallel: cfg.Load.Parallel, Calls: cfg.Load.Calls}

	if flagChanged("cps") {
		opts.CPS = callsPerSecond
	}

	if flagChanged("parallel") {
		opts.Parallel = parallelCalls
	}

	if flagChanged("calls") {
		opts.Calls = totalCalls
	}

	return opts
}

// RTPConfig — адрес и диапазон портов, на которых тестер принимает и откуда отправляет RTP
type RTPConfig struct {
	// IP — адрес, который тестер указывает в SDP и на котором слушает порты. Должен быть доступен для MM.
	IP string `json:"ip"`

	// PortMin и PortMax — диапазон UDP-портов; выдаются чётные, по одному на поток
	PortMin int `json:"port_min"`
	PortMax int `json:"port_max"`
}

const (
	defaultRTPIP      = "127.0.0.1"
	defaultRTPPortMin = 20000
	defaultRTPPortMax = 30000
)

// rtpFactory создаёт фабрику RTP-сеансов, подставляя значения по умолчанию вместо пропущенных полей
func (cfg *Config) rtpFactory() (*rtp.Factory, error) {
	ip, portMin, portMax := cfg.RTP.IP, cfg.RTP.PortMin, cfg.RTP.PortMax

	if ip == "" {
		ip = defaultRTPIP
	}

	if portMin == 0 {
		portMin = defaultRTPPortMin
	}

	if portMax == 0 {
		portMax = defaultRTPPortMax
	}

	factory, err := rtp.NewFactory(ip, portMin, portMax)
	if err != nil {
		return nil, fmt.Errorf("секция rtp: %w", err)
	}

	// каждый запуск начинает со случайного порта: см. Factory.RandomizeStart
	factory.RandomizeStart()

	return factory, nil
}

// defaultScenario — сценарий, который запускается, если он не задан ни флагом, ни конфигом
const defaultScenario = "scenario.txt"

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

// newExecutor создаёт исполнителя сценария: соединение с MM общее для всех (через link), переменные и RTP-сеансы свои
func newExecutor(link *mmlink.Link, bus *events.Bus, rtpFactory *rtp.Factory, data *protoloader.ProtoData) *scenario.Executor {
	executor := scenario.NewExecutor(nil, data.Methods, "", data.Enums)
	executor.ContinueOnError = continueOnError
	executor.SetEvents(bus)
	executor.SetRTP(rtpFactory)
	executor.SetPause(pauseMillis)

	executor.SetConnector(func() (*grpc.ClientConn, string, error) {
		session, err := link.Ensure(context.Background())
		if err != nil {
			return nil, "", err
		}

		return session.Conn, session.ID, nil
	})

	return executor
}

// connectBeforeScenario подключается к MM перед сценарием без connect(); после первого подключения ждёт startDelay.
// Для сценария с connect() ничего не делает: подключение произойдёт там, где стоит connect().
func connectBeforeScenario(link *mmlink.Link, executor *scenario.Executor, scenarioObj *scenario.Scenario, startDelay time.Duration) error {
	if scenarioObj.UsesConnect() {
		return nil
	}

	firstConnect := link.Session() == nil

	if firstConnect {
		log.Printf("В сценарии нет connect(): подключаемся к MM перед его началом")
	}

	if _, err := executor.EnsureConnected(); err != nil {
		return err
	}

	if firstConnect && startDelay > 0 {
		log.Printf("Пауза перед сценарием: %v (start_delay_sec)", startDelay)
		time.Sleep(startDelay)
	}

	return nil
}

// runScenario выполняет один сценарий. Соединение с MM устанавливается через link и общее для всех сценариев;
// переменные и накопленные события у каждого сценария свои. Подключение происходит:
//   - если в сценарии есть connect() — там, где он стоит;
//   - если нет — перед началом сценария, и после первого подключения тестер ждёт startDelay (как до появления connect()).
func runScenario(link *mmlink.Link, bus *events.Bus, rtpFactory *rtp.Factory, scenarioObj *scenario.Scenario, data *protoloader.ProtoData, startDelay time.Duration) error {
	executor := newExecutor(link, bus, rtpFactory, data)
	executor.SetInstance(1, false) // один клиент: INSTANCE = 1, лог без приставки

	if err := connectBeforeScenario(link, executor, scenarioObj, startDelay); err != nil {
		return err
	}

	return executor.Execute(scenarioObj)
}

// runScenarioLoad выполняет сценарий в нагрузочном режиме: звонки стартуют ровно по расписанию opts.CPS, одновременно не больше opts.Parallel.
// Подключение к MM и пауза start_delay_sec выполняются один раз до запуска клиентов.
// Возвращает сводку и число клиентов с ошибками.
func runScenarioLoad(link *mmlink.Link, bus *events.Bus, rtpFactory *rtp.Factory, scenarioObj *scenario.Scenario, scenarioPath string, data *protoloader.ProtoData, startDelay time.Duration, opts load.Options, stop *stopper, session *screen.Session) (string, int, error) {
	if err := connectBeforeScenario(link, newExecutor(link, bus, rtpFactory, data), scenarioObj, startDelay); err != nil {
		return "", 0, err
	}

	// события предыдущих сценариев не должны приниматься за события этого
	bus.Reset()

	// с экраном: клавиши меняют частоту и паузу через ctl, таблица команд собирается коллектором
	var (
		collector *cmdstats.Collector
		screenEnd func()
	)

	if session != nil {
		opts.Control = load.NewControl(opts.CPS)
		opts.Silent = true
		collector = cmdstats.New(scenarioObj.Outline())
		screenEnd = showScreen(session, opts, collector, rtpFactory, scenarioPath, stop)
	}

	results, elapsed, err := load.Run(opts, stop.C(), func(instance int) load.CallResult {
		executor := newExecutor(link, bus, rtpFactory, data)
		executor.SetInstance(instance, true)
		executor.SetQuiet(!verbose)

		if collector != nil {
			executor.SetObserver(collector)
		}

		// соединение с MM уже установлено в connectBeforeScenario: клиент только берёт то же самое
		if !scenarioObj.UsesConnect() {
			if _, err := executor.EnsureConnected(); err != nil {
				return load.CallResult{Err: err}
			}
		}

		err := executor.Execute(scenarioObj)

		return load.CallResult{Err: err, RTP: executor.RTPStats()}
	})
	if screenEnd != nil {
		screenEnd()
	}

	if err != nil {
		return "", 0, err
	}

	summary, failed := load.Summary(results, elapsed)

	return summary, failed, nil
}

// runLoadMode выполняет каждый сценарий в нагрузочном режиме по очереди и возвращает код завершения тестера
func runLoadMode(link *mmlink.Link, bus *events.Bus, rtpFactory *rtp.Factory, files []scenarioFile, data *protoloader.ProtoData, startDelay time.Duration, opts load.Options) int {
	stop := newStopper()

	// С экраном лог идёт в файл, а итоговые сводки печатаются после выхода с экрана
	var (
		session *screen.Session
		end     func()
	)

	if screenMode {
		var err error

		session, end, err = startScreenMode(stop)
		if err != nil {
			log.Printf("Экран не запущен: %v", err)

			return 1
		}
	}

	code := 0

	var summaries []string

	for i, file := range files {
		log.Printf("=== Нагрузка, сценарий %d из %d: %s (cps %v, одновременных звонков не больше %s, всего звонков %s) ===",
			i+1, len(files), file.Path, opts.CPS, limitText(opts.Parallel, "без ограничения"), limitText(opts.Calls, "до Ctrl+C"))

		summary, failed, err := runScenarioLoad(link, bus, rtpFactory, file.Scenario, file.Path, data, startDelay, opts, stop, session)
		if err != nil {
			summaries = append(summaries, fmt.Sprintf("Нагрузка не запущена: %v", err))
			code = 1

			break
		}

		summaries = append(summaries, summary)

		if failed > 0 {
			code = 1
		}

		if stop.Stopped() {
			break
		}
	}

	if end != nil {
		end()
	}

	for _, summary := range summaries {
		log.Print(summary)
	}

	return code
}

// chooseScenarios выбирает, что запускать: флаг -s, иначе поле scenarios из конфига, иначе scenario.txt
func chooseScenarios(fromFlag, fromConfig []string) []string {
	switch {
	case len(fromFlag) > 0:
		return fromFlag
	case len(fromConfig) > 0:
		return fromConfig
	default:
		return []string{defaultScenario}
	}
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

	rtpFactory, err := cfg.rtpFactory()
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

	// Читаем и разбираем все сценарии заранее
	// Лишние аргументы командной строки тоже считаются сценариями: так работает ./tester -p cfg.json -s scripts/*
	fromFlags := append(append([]string{}, scenarioPaths...), pflag.Args()...)

	paths, err := resolveScenarioPaths(chooseScenarios(fromFlags, cfg.Scenarios))
	if err != nil {
		log.Fatalf("Ошибка выбора сценариев: %v", err)
	}

	scenarios, err := loadScenarios(paths, scenario.NewParser(protoData.Methods))
	if err != nil {
		log.Fatalf("Ошибка загрузки сценариев:\n%v", err)
	}

	rawLoad := cfg.loadOptions(pflag.CommandLine.Changed)
	loadEnabled := rawLoad.Enabled() || screenMode

	if screenMode {
		if err := screen.NeedsTerminal(); err != nil {
			log.Fatalf("Ошибка параметров: --screen: %v", err)
		}
	}

	loadOpts, err := rawLoad.Normalize()
	if err != nil {
		log.Fatalf("Ошибка параметров нагрузки: %v", err)
	}

	// Логика подключения к MM целиком в mmlink; здесь только соединяем части
	link := mmlink.New(mmlink.NewEstablisher(protoData.Methods))

	bus := events.NewBus()

	server := &sswServer{link: link, events: bus, methods: protoData.Methods}
	go server.serve(cfg.LocalServer.Address, cfg.LocalServer.Port)

	if loadEnabled {
		finish(runLoadMode(link, bus, rtpFactory, scenarios, protoData, startDelay, loadOpts))
	}

	results := runScenarios(scenarios, func(file scenarioFile) error {
		// события предыдущих сценариев не должны приниматься за события этого
		bus.Reset()

		return runScenario(link, bus, rtpFactory, file.Scenario, protoData, startDelay)
	}, func() bool {
		return link.Session() != nil
	})

	if len(results) == 1 {
		if results[0].Err != nil {
			log.Printf("Сценарий завершился с ошибкой: %v", results[0].Err)
			finish(1)
		}

		log.Printf("Сценарий завершился успешно")
		finish(0)
	}

	summary, failed := summarize(results)
	log.Print(summary)

	if failed > 0 {
		finish(1)
	}

	finish(0)
}

// limitText показывает число или, если оно 0, подпись
func limitText(n int, zero string) string {
	if n == 0 {
		return zero
	}

	return strconv.Itoa(n)
}
