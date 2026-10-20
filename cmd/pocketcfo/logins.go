package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

const loginsFileName = "logins.json"

// loginRecord is what survives a restart, one entry per listed user.
type loginRecord struct {
	Last  time.Time `json:"last"`
	Count int       `json:"count"`
}

// loginStore remembers when each listed user last logged in — persisted to
// STATE_DIR when one is configured, in memory only otherwise — and tracks
// the sessions seen this run, so the info page can tell an active login
// from one whose session has since expired.
type loginStore struct {
	path string

	mu     sync.Mutex
	seen   map[string]time.Time
	last   map[string]time.Time
	counts map[string]int
}

func newLoginStore(stateDir string) *loginStore {
	st := &loginStore{
		seen:   map[string]time.Time{},
		last:   map[string]time.Time{},
		counts: map[string]int{},
	}
	if stateDir == "" {
		return st
	}
	st.path = filepath.Join(stateDir, loginsFileName)
	raw, err := os.ReadFile(st.path)
	if err != nil {
		return st
	}
	var recs map[string]loginRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		log.Printf("logins: %s unreadable, starting fresh: %v", st.path, err)
		return st
	}
	for email, r := range recs {
		st.last[email] = r.Last
		st.counts[email] = r.Count
	}
	return st
}

// record notes a completed login: the moment is kept even when the session
// it created has long expired.
func (st *loginStore) record(email string, at time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.last[email] = at
	st.counts[email]++
	st.seen[email] = at
	st.persistLocked()
}

// touch notes session use, in memory only: a login that keeps being used is
// active, and nothing on disk changes for it until the next real login.
func (st *loginStore) touch(email string, at time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.seen[email] = at
}

func (st *loginStore) persistLocked() {
	if st.path == "" {
		return
	}
	recs := map[string]loginRecord{}
	for email, at := range st.last {
		recs[email] = loginRecord{Last: at, Count: st.counts[email]}
	}
	buf, err := json.Marshal(recs)
	if err != nil {
		log.Printf("logins: not persisted: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		log.Printf("logins: not persisted: %v", err)
		return
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		log.Printf("logins: not persisted: %v", err)
		return
	}
	if err := os.Rename(tmp, st.path); err != nil {
		log.Printf("logins: not persisted: %v", err)
	}
}

// loginRows builds the info page's Logins group: every user from the list,
// with when they last logged in and whether a session of theirs is still
// inside its TTL.
func (s *server) loginRows() []configRow {
	st := s.logins()
	now := time.Now()

	list, err := users.Load(usersFile)
	if err != nil {
		return []configRow{{Name: "Logins", Value: "the user list is unreadable: " + err.Error()}}
	}

	out := make([]configRow, 0, len(list.Users))
	for _, u := range list.Users {
		st.mu.Lock()
		seen, hasSeen := st.seen[u.Email]
		last, hasLast := st.last[u.Email]
		count := st.counts[u.Email]
		st.mu.Unlock()

		parts := make([]string, len(u.Parts))
		for i, p := range u.Parts {
			parts[i] = string(p)
		}
		name := u.Email
		if len(parts) > 0 {
			name += " (" + joinParts(parts) + ")"
		}

		active := hasSeen && now.Sub(seen) < sessionActiveWindow
		value := "never logged in"
		if hasLast {
			value = "last login " + last.In(now.Location()).Format("02.01.2006 15:04")
			if count > 1 {
				value += fmt.Sprintf(", %d logins", count)
			}
			if active {
				value += " — active"
			} else {
				value += " — session expired"
			}
		}
		out = append(out, configRow{Name: name, Value: value})
	}
	return out
}

func joinParts(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " + " + parts[1]
	default:
		return parts[0] + " + " + parts[1] + " + …"
	}
}

// sessionActiveWindow is the read-only session TTL: a login seen more
// recently than this cannot have an expired session.
const sessionActiveWindow = auth.ReadOnlyTTL

// logins lazily builds the server's login store, since servers are also
// constructed as struct literals in tests.
func (s *server) logins() *loginStore {
	s.loginOnce.Do(func() {
		s.loginStoreData = newLoginStore(s.cfg.stateDir)
	})
	return s.loginStoreData
}
