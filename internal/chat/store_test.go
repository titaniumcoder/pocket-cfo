package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{Dir: filepath.Join(t.TempDir(), "chats")}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChatsAreCreatedListedSavedAndClosedPerUser(t *testing.T) {
	s := openStore(t)
	c, err := s.Create("Octocat")
	if err != nil {
		t.Fatal(err)
	}
	c.Title = "August statement"
	c.Messages = append(c.Messages, Message{Role: "user", Content: "hi"})
	c.Pending = append(c.Pending, PendingChange{Tool: "add_transactions", Arguments: []byte(`{}`)})
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load("octocat", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "August statement" || len(got.Messages) != 1 || len(got.Pending) != 1 || got.Version != FormatVersion {
		t.Errorf("round trip lost something: %+v", got)
	}

	list, err := s.List("octocat")
	if err != nil || len(list) != 1 || list[0].ID != c.ID || list[0].Pending != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.Load("someone-else", c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's chat must be not found, got %v", err)
	}
	if other, _ := s.List("someone-else"); len(other) != 0 {
		t.Errorf("another user sees %+v", other)
	}
	if s.Count() != 1 {
		t.Errorf("count = %d", s.Count())
	}

	if err := s.Delete("octocat", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("octocat", c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a closed chat must be gone, got %v", err)
	}
}

func TestAFormatBumpDeletesEveryChat(t *testing.T) {
	s := openStore(t)
	if _, err := s.Create("octocat"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, versionMarker), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := &Store{Dir: s.Dir}
	if err := again.Open(); err != nil {
		t.Fatal(err)
	}
	if again.Count() != 0 {
		t.Errorf("old chats survived the format bump: %d", again.Count())
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, versionMarker))
	if string(raw) != "1\n" {
		t.Errorf("marker = %q", raw)
	}
}

func TestChatFilesArePrivateToTheProcessUser(t *testing.T) {
	s := openStore(t)
	c, err := s.Create("octocat")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := os.Stat(s.userDir("octocat"))
	file, _ := os.Stat(s.path("octocat", c.ID))
	if dir.Mode().Perm() != 0o700 || file.Mode().Perm() != 0o600 {
		t.Errorf("dir %v file %v", dir.Mode().Perm(), file.Mode().Perm())
	}
}

func TestAnIdThatIsNotAnIdIsNotFoundNotAPath(t *testing.T) {
	s := openStore(t)
	for _, id := range []string{"../VERSION", "", "x", "0123456789abcdef0"} {
		if _, err := s.Load("octocat", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: got %v", id, err)
		}
		if err := s.Delete("octocat", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete %q: got %v", id, err)
		}
	}
}

func TestUserDirectoryNamesAreSafe(t *testing.T) {
	for in, want := range map[string]string{"Octocat": "octocat", "a.b@c.d": "a.b@c.d", "../x": "_x", "": "_", "über cool": "_ber_cool"} {
		if got := userDirName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestPurgeRemovesEveryUsersChats(t *testing.T) {
	s := openStore(t)
	s.Create("a")
	s.Create("b")
	n, err := s.Purge()
	if err != nil || n != 2 || s.Count() != 0 {
		t.Fatalf("purge = %d, %v, left %d", n, err, s.Count())
	}
}
