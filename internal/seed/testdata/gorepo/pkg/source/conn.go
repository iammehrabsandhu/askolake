package source

import (
	"errors"
	"fmt"
)

type Config struct {
	Host       string `json:"host"`
	MaxThreads int    `json:"max_threads,omitempty"`
	Ignored    string `json:"-"`
	ReplSlot   string `json:"replication_slot"`
}

type Conn struct{}

func (c *Conn) Slot(name string) error {
	if name == "" {
		return errors.New("replication slot name is empty")
	}
	return fmt.Errorf("failed to get replication slot %s: %w", name, errors.New("x"))
}

func short() error {
	return fmt.Errorf("bad %d", 1)
}

func Other() error {
	return fmt.Errorf("failed to get replication slot %s: %w", "a", nil)
}
