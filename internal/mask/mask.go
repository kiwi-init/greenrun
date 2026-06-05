package mask

import (
	"sort"
	"strings"
	"sync"
)

type Masker struct {
	mu     sync.RWMutex
	values []string
}

func New(values ...string) *Masker {
	m := &Masker{}
	for _, value := range values {
		m.Add(value)
	}
	return m
}

func (m *Masker) Add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.values {
		if existing == value {
			return
		}
	}
	m.values = append(m.values, value)
	sort.Slice(m.values, func(i, j int) bool { return len(m.values[i]) > len(m.values[j]) })
}

func (m *Masker) Apply(value string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, secret := range m.values {
		value = strings.ReplaceAll(value, secret, "***")
	}
	return value
}

func (m *Masker) Observe(line string) {
	const marker = "::add-mask::"
	if index := strings.Index(line, marker); index >= 0 {
		m.Add(strings.TrimSpace(line[index+len(marker):]))
	}
}
