package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
