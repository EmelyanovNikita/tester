package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tester_mm/internal/scenario"
)

// scenarioExts — расширения файлов сценариев, которые берутся из папки
var scenarioExts = []string{".txt", ".en"}

// scenarioExt — расширения для текстов справки и ошибок
var scenarioExt = strings.Join(scenarioExts, ", ")

// isScenarioFile — подходит ли имя файла для сценария из папки
func isScenarioFile(name string) bool {
	ext := filepath.Ext(name)

	for _, want := range scenarioExts {
		if ext == want {
			return true
		}
	}

	return false
}

// scenarioFile — загруженный и разобранный сценарий
type scenarioFile struct {
	Path     string
	Scenario *scenario.Scenario
}

// scenarioResult — итог выполнения одного сценария
type scenarioResult struct {
	Path     string
	Err      error
	Skipped  bool // не запускался: до него пропало соединение с MM
	Duration time.Duration
}

// resolveScenarioPaths превращает список «файл или папка» в список файлов сценариев.
// Файл берётся как есть. Из папки берутся файлы *.txt и *.en (без вложенных папок) по алфавиту.
// Порядок элементов списка сохраняется.
func resolveScenarioPaths(inputs []string) ([]string, error) {
	var paths []string

	for _, input := range inputs {
		info, err := os.Stat(input)
		if err != nil {
			return nil, fmt.Errorf("сценарий %s: %w", input, err)
		}

		if !info.IsDir() {
			paths = append(paths, input)
			continue
		}

		// ReadDir возвращает записи, отсортированные по имени файла
		entries, err := os.ReadDir(input)
		if err != nil {
			return nil, fmt.Errorf("папка %s: %w", input, err)
		}

		var found []string

		for _, entry := range entries {
			if entry.IsDir() || !isScenarioFile(entry.Name()) {
				continue
			}

			found = append(found, filepath.Join(input, entry.Name()))
		}

		if len(found) == 0 {
			return nil, fmt.Errorf("в папке %s нет файлов %s", input, scenarioExt)
		}

		paths = append(paths, found...)
	}

	if len(paths) == 0 {
		return nil, errors.New("не указано ни одного сценария")
	}

	return paths, nil
}

// loadScenarios читает и разбирает все сценарии заранее: ошибка в любом из них обнаруживается до того,
// как первый сценарий начнёт выполняться. Ошибки всех файлов возвращаются вместе.
func loadScenarios(paths []string, parser *scenario.Parser) ([]scenarioFile, error) {
	files := make([]scenarioFile, 0, len(paths))

	var problems []error

	for _, path := range paths {
		text, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			continue
		}

		parsed, err := parser.Parse(string(text))
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			continue
		}

		log.Printf("Сценарий %s загружен, команд: %d", path, len(parsed.Commands))
		files = append(files, scenarioFile{Path: path, Scenario: parsed})
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	return files, nil
}

// runScenarios выполняет сценарии по очереди. Ошибка одного сценария не мешает запуску следующих,
// кроме случая, когда соединения с MM нет совсем (connected() == false): тогда остальным нечего делать,
// и они помечаются как пропущенные.
func runScenarios(files []scenarioFile, run func(scenarioFile) error, connected func() bool) []scenarioResult {
	results := make([]scenarioResult, 0, len(files))

	for i, file := range files {
		if len(files) > 1 {
			log.Printf("=== Сценарий %d из %d: %s ===", i+1, len(files), file.Path)
		}

		start := time.Now()
		err := run(file)

		results = append(results, scenarioResult{Path: file.Path, Err: err, Duration: time.Since(start)})

		if err != nil && !connected() {
			log.Printf("Соединения с MM нет, остальные сценарии не запускаются")

			for _, rest := range files[i+1:] {
				results = append(results, scenarioResult{Path: rest.Path, Skipped: true})
			}

			break
		}
	}

	return results
}

// summarize формирует итоговую таблицу и возвращает число сценариев с ошибками
func summarize(results []scenarioResult) (string, int) {
	var (
		lines               []string
		ok, failed, skipped int
	)

	for _, result := range results {
		switch {
		case result.Skipped:
			skipped++

			lines = append(lines, fmt.Sprintf("  ПРОПУЩЕН %s", result.Path))
		case result.Err != nil:
			failed++

			lines = append(lines, fmt.Sprintf("  ОШИБКА   %s (%.1f с): %v", result.Path, result.Duration.Seconds(), result.Err))
		default:
			ok++

			lines = append(lines, fmt.Sprintf("  OK       %s (%.1f с)", result.Path, result.Duration.Seconds()))
		}
	}

	head := fmt.Sprintf("Итог: сценариев %d, успешно %d, с ошибками %d", len(results), ok, failed)
	if skipped > 0 {
		head += fmt.Sprintf(", пропущено %d", skipped)
	}

	return head + "\n" + strings.Join(lines, "\n"), failed + skipped
}
