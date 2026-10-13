package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeGitHub struct {
	files    map[string][]byte
	blobs    map[string][]byte
	deletes  []string
	puts     int
	lastBody []byte
	lastMsg  string
	lastSHA  string
	conflict bool // make the next PUT collide, as a racing writer would
}

func newFakeGitHub(files map[string]string) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}, blobs: map[string][]byte{}}
	for k, v := range files {
		f.files[k] = []byte(v)
		f.blobs[shaOf([]byte(v))] = []byte(v)
	}
	return f
}

func shaOf(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b))[:40] }

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the configured token", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		path := r.PathValue("path")

		switch r.Method {
		case http.MethodGet:
			if body, ok := f.files[path]; ok {
				json.NewEncoder(w).Encode(map[string]string{
					"content": base64.StdEncoding.EncodeToString(body),
					"sha":     shaOf(body),
				})
				return
			}
			var entries []map[string]string
			for name := range f.files {
				if rel, ok := strings.CutPrefix(name, path+"/"); ok && !strings.Contains(rel, "/") {
					entries = append(entries, map[string]string{"name": rel, "type": "file"})
				}
			}
			if entries != nil {
				json.NewEncoder(w).Encode(entries)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			f.puts++
			raw, _ := io.ReadAll(r.Body)
			var req putRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("PUT body is not JSON: %v", err)
			}
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				t.Fatalf("PUT content is not base64: %v", err)
			}
			f.lastBody, f.lastMsg, f.lastSHA = decoded, req.Message, req.SHA
			if f.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.files[path] = decoded
			f.blobs[shaOf(decoded)] = decoded
			json.NewEncoder(w).Encode(map[string]any{"content": map[string]string{"sha": shaOf(decoded)}})
		case http.MethodDelete:
			raw, _ := io.ReadAll(r.Body)
			var req deleteRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("DELETE body is not JSON: %v", err)
			}
			if body, ok := f.files[path]; !ok || shaOf(body) != req.SHA {
				w.WriteHeader(http.StatusConflict)
				return
			}
			delete(f.files, path)
			f.deletes = append(f.deletes, path)
			f.lastMsg = req.Message
			json.NewEncoder(w).Encode(map[string]any{"content": nil})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.blobs[r.PathValue("sha")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString(body), "encoding": "base64"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const writeBudgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Rent", "amount": 900 },
      { "id": "` + idGroceries + `", "name": "Groceries", "amount": 350 }
    ]}
  ]
}`

const budgetRepoPath = "data/budget.json"
