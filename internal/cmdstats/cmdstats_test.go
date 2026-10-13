package cmdstats

import (
	"errors"
	"sync"
	"testing"
)

func TestCollectorCounts(t *testing.T) {
	c := New([]Row{{Label: "1", Text: "a"}, {Label: "2", Text: "b"}, {Label: "2.1", Text: "c"}})

	c.Begin("1")
	c.Begin("1")

	if got := c.Snapshot()[0]; got.Running != 2 || got.Count != 0 {
		t.Fatalf("после двух Begin: %+v", got.Stat)
	}

	c.End("1", nil)
	c.End("1", errors.New("boom"))

	got := c.Snapshot()[0]
	if got.Running != 0 || got.Count != 2 || got.Failed != 1 {
		t.Errorf("счётчики: %+v", got.Stat)
	}

	if other := c.Snapshot()[1]; other.Count != 0 || other.Running != 0 {
		t.Errorf("чужая команда изменилась: %+v", other.Stat)
	}
}

func TestCollectorIgnoresUnknownLabel(t *testing.T) {
	c := New([]Row{{Label: "1"}})
	c.Begin("9")
	c.End("9", nil)

	if got := c.Snapshot(); len(got) != 1 || got[0].Count != 0 {
		t.Errorf("неизвестный номер изменил таблицу: %+v", got)
	}
}

func TestCollectorKeepsOrder(t *testing.T) {
	rows := []Row{{Label: "2"}, {Label: "1"}, {Label: "10"}}
	got := New(rows).Snapshot()

	for i, row := range rows {
		if got[i].Label != row.Label {
			t.Errorf("позиция %d: %q, ожидалось %q", i, got[i].Label, row.Label)
		}
	}
}

func TestCollectorConcurrent(t *testing.T) {
	c := New([]Row{{Label: "1"}})

	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			c.Begin("1")
			c.End("1", nil)
		}()
	}

	wg.Wait()

	if got := c.Snapshot()[0]; got.Count != 50 || got.Running != 0 {
		t.Errorf("%+v", got.Stat)
	}
}
