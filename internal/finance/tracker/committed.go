package tracker

import (
	"bytes"
	"sync"
)

type committed struct {
	mu    sync.Mutex
	byKey map[string][]byte
}

func (c *committed) publish(key string, body []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey == nil {
		c.byKey = map[string][]byte{}
	}
	c.byKey[key] = bytes.Clone(body)
}

func (c *committed) bytesFor(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.byKey[key]
	return body, ok
}

func (c *committed) forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byKey, key)
}

func (c *committed) supersedes(key string, onDisk []byte, readErr error) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	body, ok := c.byKey[key]
	if !ok {
		return nil, false
	}
	if readErr == nil && bytes.Equal(bytes.TrimSpace(onDisk), bytes.TrimSpace(body)) {
		delete(c.byKey, key)
		return nil, false
	}
	return body, true
}
