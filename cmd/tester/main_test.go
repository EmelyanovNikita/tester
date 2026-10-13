package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tester_mm/internal/protoloader"
	"tester_mm/internal/scenario"
)

func writeConfig(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("запись конфига: %v", err)
	}

	return path
}

func TestStartDelay(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    time.Duration
		wantErr string
	}{
		{"не задано — прежние 3 секунды", `{}`, 3 * time.Second, ""},
		{"задано", `{"start_delay_sec": 1.5}`, 1500 * time.Millisecond, ""},
		{"ноль отключает паузу", `{"start_delay_sec": 0}`, 0, ""},
		{"отрицательное", `{"start_delay_sec": -1}`, 0, "start_delay_sec"},
		{"слишком большое", `{"start_delay_sec": 100000}`, 0, "start_delay_sec"},
	}

	for _, c := range cases {
		cfg, err := loadConfig(writeConfig(t, c.json))
		if err != nil {
			t.Errorf("%s: loadConfig: %v", c.name, err)
			continue
		}

		got, err := cfg.startDelay()

		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.wantErr, err)
			}

			continue
		}

		if err != nil || got != c.want {
			t.Errorf("%s: пауза %v (ошибка %v), ожидалось %v", c.name, got, err, c.want)
		}
	}
}

func TestLoadConfigFields(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{
		"proto_root": "/p",
		"proto_files": ["a.proto", "b.proto"],
		"local_server": {"address": "0.0.0.0", "port": 7000}
	}`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.ProtoRoot != "/p" || len(cfg.ProtoFiles) != 2 || cfg.LocalServer.Port != 7000 {
		t.Errorf("конфиг разобран неверно: %+v", cfg)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	if _, err := loadConfig(filepath.Join(t.TempDir(), "нет-такого.json")); err == nil {
		t.Error("ожидалась ошибка для несуществующего файла")
	}

	if _, err := loadConfig(writeConfig(t, `{не json`)); err == nil {
		t.Error("ожидалась ошибка для неверного JSON")
	}
}

// ===== Выбор и запуск нескольких сценариев =====

func writeFile(t *testing.T, path, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("создание папки: %v", err)
	}

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("запись файла: %v", err)
	}
}

func TestChooseScenarios(t *testing.T) {
	cases := []struct {
		name       string
		flag, conf []string
		want       []string
	}{
		{"флаг главнее конфига", []string{"a"}, []string{"b"}, []string{"a"}},
		{"конфиг, если флага нет", nil, []string{"b", "c"}, []string{"b", "c"}},
		{"по умолчанию", nil, nil, []string{"scenario.txt"}},
	}

	for _, c := range cases {
		if got := chooseScenarios(c.flag, c.conf); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

func TestResolveScenarioPaths(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "many", "b.txt"), "x = 1\n")
	writeFile(t, filepath.Join(dir, "many", "a.txt"), "x = 1\n")
	writeFile(t, filepath.Join(dir, "many", "notes.md"), "не сценарий\n")
	writeFile(t, filepath.Join(dir, "many", "nested", "c.txt"), "x = 1\n") // вложенные папки не обходятся
	writeFile(t, filepath.Join(dir, "single.txt"), "x = 1\n")
	writeFile(t, filepath.Join(dir, "other.script"), "x = 1\n") // файл берётся как есть, расширение не важно

	got, err := resolveScenarioPaths([]string{
		filepath.Join(dir, "single.txt"),
		filepath.Join(dir, "many"),
		filepath.Join(dir, "other.script"),
	})
	if err != nil {
		t.Fatalf("resolveScenarioPaths: %v", err)
	}

	want := []string{
		filepath.Join(dir, "single.txt"),
		filepath.Join(dir, "many", "a.txt"),
		filepath.Join(dir, "many", "b.txt"),
		filepath.Join(dir, "other.script"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("пути %v, ожидалось %v", got, want)
	}
}

func TestResolveScenarioPathsErrors(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "empty", "readme.md"), "нет сценариев\n")

	cases := []struct {
		name   string
		inputs []string
		want   string
	}{
		{"нет файла", []string{filepath.Join(dir, "нет.txt")}, "нет.txt"},
		{"папка без .txt", []string{filepath.Join(dir, "empty")}, "нет файлов .txt"},
		{"пустой список", nil, "не указано ни одного сценария"},
	}

	for _, c := range cases {
		_, err := resolveScenarioPaths(c.inputs)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.want, err)
		}
	}
}

func TestLoadScenarios(t *testing.T) {
	dir := t.TempDir()
	parser := scenario.NewParser(map[string]protoloader.MethodInfo{})

	good1 := filepath.Join(dir, "good1.txt")
	good2 := filepath.Join(dir, "good2.txt")
	bad1 := filepath.Join(dir, "bad1.txt")
	bad2 := filepath.Join(dir, "bad2.txt")
	missing := filepath.Join(dir, "missing.txt")

	writeFile(t, good1, "x = 1\ny = 2\n")
	writeFile(t, good2, "for i in 1..2 {\n    z = i\n}\n")
	writeFile(t, bad1, "x = 1\n}\n")
	writeFile(t, bad2, "for i in 1..2 {\n")

	files, err := loadScenarios([]string{good1, good2}, parser)
	if err != nil || len(files) != 2 || files[0].Path != good1 || len(files[0].Scenario.Commands) != 2 {
		t.Fatalf("корректные сценарии: files=%+v err=%v", files, err)
	}

	// ошибки всех плохих файлов возвращаются вместе, хорошие не мешают
	_, err = loadScenarios([]string{good1, bad1, bad2, missing}, parser)
	if err == nil {
		t.Fatal("ожидалась ошибка загрузки")
	}

	for _, want := range []string{bad1, bad2, missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет файла %s: %v", want, err)
		}
	}

	if strings.Contains(err.Error(), good1) {
		t.Errorf("в ошибке упомянут корректный файл: %v", err)
	}
}

func files3() []scenarioFile {
	return []scenarioFile{{Path: "a.txt"}, {Path: "b.txt"}, {Path: "c.txt"}}
}

func TestRunScenariosContinuesAfterFailure(t *testing.T) {
	var ran []string

	results := runScenarios(files3(), func(file scenarioFile) error {
		ran = append(ran, file.Path)

		if file.Path == "b.txt" {
			return errors.New("упал")
		}

		return nil
	}, func() bool { return true })

	if !reflect.DeepEqual(ran, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Errorf("запущены %v: ошибка одного сценария не должна мешать следующим", ran)
	}

	if len(results) != 3 || results[0].Err != nil || results[1].Err == nil || results[2].Err != nil {
		t.Errorf("результаты: %+v", results)
	}
}

func TestRunScenariosStopsWithoutConnection(t *testing.T) {
	var ran []string

	results := runScenarios(files3(), func(file scenarioFile) error {
		ran = append(ran, file.Path)

		return errors.New("подключение к MM: нет соединения")
	}, func() bool { return false })

	if !reflect.DeepEqual(ran, []string{"a.txt"}) {
		t.Errorf("запущены %v: без соединения остальные сценарии запускать не нужно", ran)
	}

	if len(results) != 3 || results[0].Err == nil || !results[1].Skipped || !results[2].Skipped {
		t.Errorf("результаты: %+v", results)
	}
}

func TestSummarize(t *testing.T) {
	results := []scenarioResult{
		{Path: "a.txt", Duration: 1500 * time.Millisecond},
		{Path: "b.txt", Err: errors.New("сценарий остановлен на команде 2"), Duration: time.Second},
		{Path: "c.txt", Skipped: true},
	}

	text, failed := summarize(results)

	// ошибочные и пропущенные сценарии дают ненулевой код завершения
	if failed != 2 {
		t.Errorf("failed = %d, ожидалось 2", failed)
	}

	for _, want := range []string{
		"сценариев 3, успешно 1, с ошибками 1, пропущено 1",
		"OK       a.txt (1.5 с)",
		"ОШИБКА   b.txt (1.0 с): сценарий остановлен на команде 2",
		"ПРОПУЩЕН c.txt",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в итоге нет %q:\n%s", want, text)
		}
	}

	allOK, failed := summarize([]scenarioResult{{Path: "a.txt"}, {Path: "b.txt"}})
	if failed != 0 || strings.Contains(allOK, "пропущено") {
		t.Errorf("все успешно: failed=%d\n%s", failed, allOK)
	}
}

func TestRTPFactoryConfig(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantIP  string
		wantErr string
	}{
		{"не задано — значения по умолчанию", `{}`, "127.0.0.1", ""},
		{"задан только ip", `{"rtp": {"ip": "10.0.0.7"}}`, "10.0.0.7", ""},
		{"задан диапазон", `{"rtp": {"port_min": 40000, "port_max": 40100}}`, "127.0.0.1", ""},
		{"неверный ip", `{"rtp": {"ip": "не-адрес"}}`, "", "секция rtp"},
		{"перевёрнутый диапазон", `{"rtp": {"port_min": 40100, "port_max": 40000}}`, "", "секция rtp"},
		{"порт вне диапазона", `{"rtp": {"port_min": 100, "port_max": 70000}}`, "", "секция rtp"},
	}

	for _, c := range cases {
		cfg, err := loadConfig(writeConfig(t, c.json))
		if err != nil {
			t.Errorf("%s: loadConfig: %v", c.name, err)
			continue
		}

		factory, err := cfg.rtpFactory()

		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: ожидалась ошибка с %q, получили: %v", c.name, c.wantErr, err)
			}

			continue
		}

		if err != nil || factory.IP() != c.wantIP {
			t.Errorf("%s: ip %v (ошибка %v), ожидалось %s", c.name, factory, err, c.wantIP)
		}
	}
}

func TestLoadOptions(t *testing.T) {
	oldCPS, oldParallel, oldCalls := callsPerSecond, parallelCalls, totalCalls
	defer func() { callsPerSecond, parallelCalls, totalCalls = oldCPS, oldParallel, oldCalls }()

	cfg := &Config{Load: LoadConfig{CPS: 5, Parallel: 50, Calls: 500}}

	// флаги не указаны: берётся конфиг
	callsPerSecond, parallelCalls, totalCalls = 0, 0, 0

	if got := cfg.loadOptions(func(string) bool { return false }); got.CPS != 5 || got.Parallel != 50 || got.Calls != 500 {
		t.Errorf("из конфига: %+v", got)
	}

	// указанный флаг перекрывает конфиг, остальные поля остаются из конфига
	callsPerSecond, parallelCalls, totalCalls = 20, 100, 0

	got := cfg.loadOptions(func(name string) bool { return name == "parallel" || name == "cps" })
	if got.CPS != 20 || got.Parallel != 100 || got.Calls != 500 {
		t.Errorf("флаги поверх конфига: %+v", got)
	}

	// флаг со значением по умолчанию, указанный явно (--calls 0), тоже перекрывает конфиг
	got = cfg.loadOptions(func(name string) bool { return name == "calls" })
	if got.Calls != 0 || got.Parallel != 50 {
		t.Errorf("явный --calls 0: %+v", got)
	}

	// без конфига и флагов нагрузочный режим выключен
	if (&Config{}).loadOptions(func(string) bool { return false }).Enabled() {
		t.Errorf("по умолчанию нагрузка должна быть выключена")
	}
}

func TestLoadConfigLoadSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"load": {"parallel": 100, "calls": 300, "cps": 12.5}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Load.Parallel != 100 || cfg.Load.Calls != 300 || cfg.Load.CPS != 12.5 {
		t.Errorf("секция load: %+v", cfg.Load)
	}
}
