package api

import (
	"context"
	"strings"
	"testing"
)

func TestLineDiffShowsChangedLinesWithContext(t *testing.T) {
	before := "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3,\n  \"d\": 4,\n  \"e\": 5,\n  \"f\": 6,\n  \"g\": 7\n}\n"
	after := strings.Replace(before, "\"e\": 5", "\"e\": 50", 1)
	diff, changed := LineDiff(before, after)
	if changed != 2 || !strings.Contains(diff, "-  \"e\": 5,\n+  \"e\": 50,\n") || !strings.Contains(diff, "@@ -3 +3 @@") {
		t.Errorf("changed=%d\n%s", changed, diff)
	}
	if strings.Contains(diff, "\"a\": 1") {
		t.Error("lines beyond the context must not appear")
	}
	if d, n := LineDiff("", "x\n"); n != 1 || !strings.Contains(d, "+x") {
		t.Errorf("new file: %d %q", n, d)
	}
}

func TestDataFilesAreAnAllowList(t *testing.T) {
	svc := &Service{}
	_, err := svc.ReadDataFile(context.Background(), "users.json")
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest || !strings.Contains(e.Message, "accounts.json, budget.json, config.json") {
		t.Fatalf("got %v", err)
	}
	if _, err := svc.ReadDataFile(context.Background(), FileBudget); err.(*Error).Code != CodeWriteNotConfigured {
		t.Errorf("without a store: %v", err)
	}
}

func TestWriteDataFileValidatesThenCommitsWithADiff(t *testing.T) {
	gh := newFakeGitHub(map[string]string{"data/budget.json": writeBudgetJSON})
	svc := writeService(t, gh)

	got, err := svc.ReadDataFile(context.Background(), FileBudget)
	if err != nil || got.Path != "data/budget.json" || !strings.Contains(got.Content, "Groceries") || got.SHA == "" {
		t.Fatalf("read = %+v %v", got, err)
	}

	broken := strings.Replace(got.Content, `"amount": 350`, `"amount": -350`, 1)
	_, err = svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileBudget, Content: broken, Reason: "negative"})
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed || gh.puts != 0 {
		t.Fatalf("an invalid budget must be refused before any commit: %v, puts %d", err, gh.puts)
	}
	_, err = svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileBudget, Content: "{not json", Reason: "x"})
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
		t.Errorf("not json: %v", err)
	}
	_, err = svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileBudget, Content: got.Content, Reason: ""})
	if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
		t.Errorf("no reason: %v", err)
	}

	same, err := svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileBudget, Content: got.Content, Reason: "nothing"})
	if err != nil || !same.Unchanged || gh.puts != 0 {
		t.Fatalf("identical content commits nothing: %+v %v puts %d", same, err, gh.puts)
	}

	changed := strings.Replace(got.Content, `"amount": 350`, `"amount": 400`, 1)
	out, err := svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileBudget, Content: changed, Reason: "groceries went up"})
	if err != nil {
		t.Fatal(err)
	}
	if gh.puts != 1 || out.ChangedLines != 2 || !strings.Contains(out.Diff, "+") || !strings.Contains(gh.lastMsg, "chore(data): budget.json — groceries went up") {
		t.Errorf("out = %+v, msg %q", out, gh.lastMsg)
	}
	if !strings.Contains(string(gh.files["data/budget.json"]), `"amount": 400`) {
		t.Error("the file was not committed")
	}
	cats, err := svc.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 {
		t.Errorf("the written budget must read back at once: %+v", cats)
	}
}

func TestConfigFileMustParseAsTheRulesItCarries(t *testing.T) {
	gh := newFakeGitHub(map[string]string{"config.json": `{"hoursPerDay": 8}`})
	svc := writeService(t, gh)
	_, err := svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileConfig, Content: `{"hoursPerDay": 8, "legislation": [{"from": "not-a-month"}]}`, Reason: "x"})
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
		t.Errorf("a config that cannot load must be refused: %v", err)
	}
	out, err := svc.WriteDataFile(context.Background(), FileWriteRequest{Name: FileConfig, Content: `{"hoursPerDay": 7}`, Reason: "shorter days"})
	if err != nil || out.ChangedLines == 0 {
		t.Errorf("a good config commits: %+v %v", out, err)
	}
}
