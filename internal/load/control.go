package load

import (
	"sync"
	"time"
)

const (
	// minCPS — нижняя граница частоты при изменении во время теста (клавиша «-»); чтобы остановить запуски, есть пауза
	minCPS = 0.1

	// pollInterval — как часто запуски проверяют, не изменилась ли частота или пауза
	pollInterval = 20 * time.Millisecond
)

// Control — управление нагрузкой во время теста и счётчики для экрана.
// Безопасен для вызова из нескольких горутин. Частоту и паузу можно менять, пока идёт Run.
type Control struct {
	mu     sync.Mutex
	cps    float64
	paused bool

	begin    time.Time
	started  int64
	finished int64
	failed   int64
	durSum   time.Duration
	durMin   time.Duration
	durMax   time.Duration
	lastErr  string
}

// Counters — снимок счётчиков теста
type Counters struct {
	Elapsed  time.Duration
	Started  int64 // сколько звонков запущено
	Running  int64 // сколько идёт сейчас
	Finished int64 // сколько завершено (успешно и с ошибкой)
	Failed   int64 // из завершённых — с ошибкой

	// Длительность завершённых звонков; нули, пока ни один не завершился
	DurMin, DurAvg, DurMax time.Duration

	LastError string // текст последней ошибки звонка (пусто, если ошибок не было)
}

// NewControl создаёт управление с начальной частотой cps звонков в секунду
func NewControl(cps float64) *Control {
	return &Control{cps: clampCPS(cps)}
}

func clampCPS(cps float64) float64 {
	switch {
	case cps < minCPS:
		return minCPS
	case cps > maxCPS:
		return maxCPS
	}

	return cps
}

// CPS возвращает текущую целевую частоту запусков
func (c *Control) CPS() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cps
}

// SetCPS задаёт частоту; значение ограничивается диапазоном от 0.1 до максимума
func (c *Control) SetCPS(cps float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cps = clampCPS(cps)
}

// AddCPS изменяет частоту на delta (может быть отрицательной)
func (c *Control) AddCPS(delta float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cps = clampCPS(c.cps + delta)
}

// Paused сообщает, что запуски новых звонков приостановлены
func (c *Control) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.paused
}

// TogglePause переключает паузу и возвращает новое состояние. Идущие звонки продолжаются.
func (c *Control) TogglePause() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.paused = !c.paused

	return c.paused
}

// Counters возвращает снимок счётчиков
func (c *Control) Counters() Counters {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := Counters{
		Started:   c.started,
		Running:   c.started - c.finished,
		Finished:  c.finished,
		Failed:    c.failed,
		DurMin:    c.durMin,
		DurMax:    c.durMax,
		LastError: c.lastErr,
	}

	if !c.begin.IsZero() {
		snapshot.Elapsed = time.Since(c.begin)
	}

	if c.finished > 0 {
		snapshot.DurAvg = c.durSum / time.Duration(c.finished)
	}

	return snapshot
}

// start запоминает момент начала теста
func (c *Control) start(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.begin = now
}

func (c *Control) callStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.started++
}

func (c *Control) callFinished(result CallResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.finished++
	c.durSum += result.Duration

	if c.finished == 1 || result.Duration < c.durMin {
		c.durMin = result.Duration
	}

	if result.Duration > c.durMax {
		c.durMax = result.Duration
	}

	if result.Err != nil {
		c.failed++
		c.lastErr = result.Err.Error()
	}
}
