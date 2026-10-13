// Package cmdstats — статистика выполнения команд сценария по всем клиентам (для экрана нагрузки).
//
// Коллектор получает от исполнителей сценария события «команда началась» и «команда закончилась» (Begin, End)
// и копит по каждой команде: сколько клиентов выполняют её сейчас, сколько раз она выполнена, сколько раз с ошибкой
// и длительность. Команды опознаются по номеру (label): 1, 2, 2.1 внутри for/if, 2.else.1 в ветке else.
// Пакет не знает про сценарии: список строк для показа ему передаёт вызывающий.
package cmdstats

import (
	"sync"
	"time"
)

// Row — строка таблицы: одна команда сценария
type Row struct {
	Label string // номер команды, как в логе: 1, 2.1, 2.else.1
	Depth int    // вложенность в for/if (для отступа)
	Text  string // текст команды из сценария
}

// Stat — накопленные числа по одной команде
type Stat struct {
	Running int64         // сколько клиентов выполняют команду прямо сейчас
	Count   int64         // сколько раз команда завершилась (успешно и с ошибкой)
	Failed  int64         // из них с ошибкой
	Min     time.Duration // длительность одного выполнения
	Max     time.Duration
	Total   time.Duration
}

// Avg возвращает среднюю длительность; 0, если команда ещё не завершалась
func (s Stat) Avg() time.Duration {
	if s.Count == 0 {
		return 0
	}

	return s.Total / time.Duration(s.Count)
}

// RowStat — строка таблицы вместе с её числами
type RowStat struct {
	Row
	Stat
}

// Collector безопасен для вызова из нескольких горутин
type Collector struct {
	mu    sync.Mutex
	rows  []Row
	stats map[string]*Stat
}

// New создаёт коллектор для заданных строк (порядок строк — порядок показа)
func New(rows []Row) *Collector {
	c := &Collector{rows: rows, stats: make(map[string]*Stat, len(rows))}

	for _, row := range rows {
		c.stats[row.Label] = &Stat{}
	}

	return c
}

// Begin отмечает, что клиент начал выполнять команду label. Неизвестные номера игнорируются.
func (c *Collector) Begin(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if stat, ok := c.stats[label]; ok {
		stat.Running++
	}
}

// End отмечает, что клиент закончил команду label за elapsed; err != nil — с ошибкой
func (c *Collector) End(label string, elapsed time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stat, ok := c.stats[label]
	if !ok {
		return
	}

	if stat.Running > 0 {
		stat.Running--
	}

	stat.Count++
	stat.Total += elapsed

	if stat.Count == 1 || elapsed < stat.Min {
		stat.Min = elapsed
	}

	if elapsed > stat.Max {
		stat.Max = elapsed
	}

	if err != nil {
		stat.Failed++
	}
}

// Snapshot возвращает копию таблицы в порядке показа
func (c *Collector) Snapshot() []RowStat {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]RowStat, 0, len(c.rows))

	for _, row := range c.rows {
		out = append(out, RowStat{Row: row, Stat: *c.stats[row.Label]})
	}

	return out
}
