package scenario

import (
	"sync"
	"testing"

	"tester_mm/internal/cmdstats"
)

const outlineScript = `x = 1
for i in 1..3 {
    y = i
    sleep(0)
}
if x == 1 {
    pause(2)
    log("then")
} else {
    sleep(3)
    pause(4)
}
pause(1)
`

func TestOutline(t *testing.T) {
	parsed, err := NewParser(nil).Parse(outlineScript)
	if err != nil {
		t.Fatal(err)
	}

	// присваивания, log, for и if в таблицу не попадают; команды внутри блоков попадают с номерами из лога
	want := []cmdstats.Row{
		{Label: "2.2", Text: "sleep(0)"},
		{Label: "3.1", Text: "pause(2)"},
		{Label: "3.else.1", Text: "sleep(3)"},
		{Label: "3.else.2", Text: "pause(4)"},
		{Label: "4", Text: "pause(1)"},
	}

	got := parsed.Outline()
	if len(got) != len(want) {
		t.Fatalf("строк %d, ожидалось %d: %+v", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("строка %d: %+v, ожидалось %+v", i, got[i], want[i])
		}
	}
}

// Наблюдатель получает те же номера, что в Outline: счётчики по каждой команде сходятся с ходом сценария
func TestObserverMatchesOutline(t *testing.T) {
	parsed, err := NewParser(nil).Parse(outlineScript)
	if err != nil {
		t.Fatal(err)
	}

	collector := cmdstats.New(parsed.Outline())

	executor := NewExecutor(nil, nil, "", nil)
	executor.SetQuiet(true)
	executor.SetObserver(collector)

	if err := executor.Execute(parsed); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int64{}
	for _, row := range collector.Snapshot() {
		counts[row.Label] = row.Count

		if row.Running != 0 {
			t.Errorf("команда %s: идёт %d после конца сценария", row.Label, row.Running)
		}
	}

	// x == 1: ветка then выполнилась, else — нет; pause без -d пропускается, но считается выполненной
	want := map[string]int64{"2.2": 3, "3.1": 1, "3.else.1": 0, "3.else.2": 0, "4": 1}
	for label, n := range want {
		if counts[label] != n {
			t.Errorf("команда %s: выполнена %d раз, ожидалось %d", label, counts[label], n)
		}
	}
}

type recordingObserver struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingObserver) Begin(label string) {
	r.mu.Lock()
	r.events = append(r.events, "begin "+label)
	r.mu.Unlock()
}

func (r *recordingObserver) End(label string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err != nil {
		label += " ошибка"
	}

	r.events = append(r.events, "end "+label)
}

func TestObserverReportsErrors(t *testing.T) {
	parsed, err := NewParser(nil).Parse("x = 1\nbroken = nothing_here\n")
	if err != nil {
		t.Fatal(err)
	}

	observer := &recordingObserver{}

	executor := NewExecutor(nil, nil, "", nil)
	executor.SetQuiet(true)
	executor.SetObserver(observer)

	if err := executor.Execute(parsed); err == nil {
		t.Fatal("ожидалась ошибка сценария")
	}

	want := []string{"begin 1", "end 1", "begin 2", "end 2 ошибка"}
	if len(observer.events) != len(want) {
		t.Fatalf("события: %v", observer.events)
	}

	for i := range want {
		if observer.events[i] != want[i] {
			t.Errorf("событие %d: %q, ожидалось %q", i, observer.events[i], want[i])
		}
	}
}
