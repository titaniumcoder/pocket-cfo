package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const FormatVersion = 1

const versionMarker = "VERSION"

var ErrNotFound = errors.New("chat not found")

var chatIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

type Chat struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Login     string          `json:"login"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Messages  []Message       `json:"messages"`
	Files     []File          `json:"files,omitempty"`
	Pending   []PendingChange `json:"pending,omitempty"`
	Applied   []AppliedChange `json:"applied,omitempty"`
	Question  *Question       `json:"question,omitempty"`
	Usage     Usage           `json:"usage"`
}

type Question struct {
	ToolCallID    string   `json:"tool_call_id"`
	Text          string   `json:"text"`
	Options       []string `json:"options,omitempty"`
	AllowFreeText bool     `json:"allow_free_text"`
}

type File struct {
	Name    string `json:"name"`
	Bytes   int    `json:"bytes"`
	Message int    `json:"message"`
}

type PendingChange struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Summary   string          `json:"summary,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type AppliedChange struct {
	Path      string    `json:"path"`
	Message   string    `json:"message"`
	BeforeSHA string    `json:"before_sha,omitempty"`
	AfterSHA  string    `json:"after_sha"`
	Created   bool      `json:"created,omitempty"`
	At        time.Time `json:"at"`
	Reverted  bool      `json:"reverted,omitempty"`
}

type Summary struct {
	ID        string
	Title     string
	UpdatedAt time.Time
	Pending   int
}

type Store struct {
	Dir string

	locks sync.Map
}

func (s *Store) Open() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("chat: creating %s: %w", s.Dir, err)
	}
	return s.ensureVersion()
}

func (s *Store) ensureVersion() error {
	marker := filepath.Join(s.Dir, versionMarker)
	want := strconv.Itoa(FormatVersion)
	if raw, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(raw)) == want {
		return nil
	}
	removed, err := s.Purge()
	if err != nil {
		return err
	}
	if err := writeAtomically(marker, []byte(want+"\n"), 0o600); err != nil {
		return fmt.Errorf("chat: writing %s: %w", marker, err)
	}
	if removed > 0 {
		log.Printf("chat: %s is not format version %s — removed %d chat(s) and marked it", s.Dir, want, removed)
	}
	return nil
}

func (s *Store) Purge() (int, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0, fmt.Errorf("chat: reading %s: %w", s.Dir, err)
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		userDir := filepath.Join(s.Dir, e.Name())
		files, _ := os.ReadDir(userDir)
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".json") {
				removed++
			}
		}
		if err := os.RemoveAll(userDir); err != nil {
			return removed, fmt.Errorf("chat: removing %s: %w", userDir, err)
		}
	}
	return removed, nil
}

func (s *Store) Count() int {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(s.Dir, e.Name()))
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".json") {
				n++
			}
		}
	}
	return n
}

func (s *Store) List(login string) ([]Summary, error) {
	entries, err := os.ReadDir(s.userDir(login))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if !chatIDRE.MatchString(id) {
			continue
		}
		c, err := s.Load(login, id)
		if err != nil {
			log.Printf("chat: skipping %s: %v", e.Name(), err)
			continue
		}
		out = append(out, Summary{ID: c.ID, Title: c.Title, UpdatedAt: c.UpdatedAt, Pending: len(c.Pending)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Store) Create(login string) (*Chat, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	now := time.Now()
	c := &Chat{
		Version:   FormatVersion,
		ID:        hex.EncodeToString(b[:]),
		Login:     login,
		Title:     "New chat",
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{},
	}
	if err := s.Save(c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) Load(login, id string) (*Chat, error) {
	if !chatIDRE.MatchString(id) {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(s.path(login, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("chat %s is not readable: %w", id, err)
	}
	if userDirName(c.Login) != userDirName(login) {
		return nil, ErrNotFound
	}
	if c.Messages == nil {
		c.Messages = []Message{}
	}
	return &c, nil
}

func (s *Store) Save(c *Chat) error {
	if !chatIDRE.MatchString(c.ID) {
		return fmt.Errorf("chat: %q is not a chat id", c.ID)
	}
	c.Version = FormatVersion
	c.UpdatedAt = time.Now()
	if err := os.MkdirAll(s.userDir(c.Login), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomically(s.path(c.Login, c.ID), raw, 0o600)
}

func (s *Store) Delete(login, id string) error {
	if !chatIDRE.MatchString(id) {
		return ErrNotFound
	}
	if _, err := s.Load(login, id); err != nil {
		return err
	}
	return os.Remove(s.path(login, id))
}

func (s *Store) Lock(id string) func() {
	mu, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

func (s *Store) userDir(login string) string {
	return filepath.Join(s.Dir, userDirName(login))
}

func (s *Store) path(login, id string) string {
	return filepath.Join(s.userDir(login), id+".json")
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9._@-]+`)

func userDirName(login string) string {
	name := unsafeChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(login)), "_")
	name = strings.Trim(name, ".")
	if name == "" {
		return "_"
	}
	return name
}

func writeAtomically(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
