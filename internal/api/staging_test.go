package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func stagedService(t *testing.T, gh *fakeGitHub) (*Service, *Staging) {
	t.Helper()
	svc := writeService(t, gh)
	st := NewStaging(svc.Store)
	svc.Store = st
	return svc, st
}

func call(name, args string) ToolCall { return ToolCall{Name: name, Arguments: json.RawMessage(args)} }

const augustAdd = `{"transactions":[{"id":"n1","date":"2026-08-03","description":"NEW","amount":12,"account":"A","category":"` + idGroceries + `"}]}`

func TestReplayStagesWritesWithoutCommittingAndReadsThemBack(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	svc, st := stagedService(t, gh)

	staged := Replay(context.Background(), svc, []ToolCall{
		call("add_transactions", augustAdd),
		call("edit_transactions", `{"edits":[{"id":"t1","month":"2026-08","category":"`+idGroceries+`"}],"reason":"was groceries"}`),
	})
	for i, s := range staged {
		if s.Err != nil {
			t.Fatalf("call %d: %v", i, s.Err)
		}
	}
	if gh.puts != 0 {
		t.Fatalf("staging reached GitHub: %d puts", gh.puts)
	}

	files := st.Files()
	if len(files) != 1 || files[0].Path != augustPath || len(files[0].Messages) != 2 || files[0].Created {
		t.Fatalf("pending files = %+v", files)
	}
	body := string(files[0].Content)
	if !strings.Contains(body, `"n1"`) || !strings.Contains(body, `"id": "t1"`) || strings.Count(body, idGroceries) != 2 {
		t.Errorf("the staged month must carry both changes:\n%s", body)
	}

	month, err := svc.ActualsFor(context.Background(), "2026-08")
	if err != nil || len(month.Transactions) != 2 {
		t.Fatalf("a read through the staged store must see the staged month: %+v %v", month, err)
	}
}

func TestReplayRecordsAFailurePerCallAndKeepsGoing(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	svc, st := stagedService(t, gh)

	staged := Replay(context.Background(), svc, []ToolCall{
		call("get_actuals", `{"month":"2026-08"}`),
		call("add_transactions", `{"transactions":[{"id":"x","date":"2026-08-03","description":"X","amount":1,"account":"A","category":"nope"}]}`),
		call("add_transactions", augustAdd),
		call("no_such_tool", `{}`),
	})
	if staged[0].Err == nil || !strings.Contains(staged[0].Err.Message, "cannot be pending") {
		t.Errorf("a read tool is not a change: %+v", staged[0])
	}
	if staged[1].Err == nil || staged[1].Err.Code != CodeValidationFailed && staged[1].Err.Code != CodeInvalidRequest {
		t.Errorf("an unknown category must fail its own call: %+v", staged[1])
	}
	if staged[2].Err != nil {
		t.Errorf("the good call must still run: %v", staged[2].Err)
	}
	if staged[3].Err == nil {
		t.Errorf("an unknown tool must fail: %+v", staged[3])
	}
	if len(st.Files()) != 1 {
		t.Errorf("only the good call is pending: %+v", st.Files())
	}
}

func TestFlushCommitsOnceANDPublishesNothingItself(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	svc, st := stagedService(t, gh)
	Replay(context.Background(), svc, []ToolCall{
		call("add_transactions", augustAdd),
		call("edit_transactions", `{"edits":[{"id":"t1","month":"2026-08","category":"`+idGroceries+`"}],"reason":"was groceries"}`),
		call("add_transactions", `{"transactions":[{"id":"s1","date":"2026-09-02","description":"SEPT","amount":5,"account":"A","category":"`+idRent+`"}],"coverage":[{"account":"A","from":"2026-09-01","to":"2026-09-30","imported_at":"2026-10-01"}]}`),
	})

	receipts, err := st.Flush(context.Background(), st.base, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if gh.puts != 2 || len(receipts) != 2 {
		t.Fatalf("want one commit per touched file, got %d puts and %d receipts", gh.puts, len(receipts))
	}
	august, september := receipts[0], receipts[1]
	if august.Created || august.BeforeSHA == "" || august.AfterSHA == "" || !strings.Contains(august.Message, "+1 more change") || !strings.Contains(august.Message, "Approved in the chat by octocat") {
		t.Errorf("august receipt = %+v", august)
	}
	if !september.Created || september.BeforeSHA != "" || !strings.HasPrefix(september.Message, "feat(actuals): add 1 transaction to 2026-09") {
		t.Errorf("september receipt = %+v", september)
	}
}

func TestFlushOnAStaleBaseIsAConflictThatNamesThePath(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	svc, st := stagedService(t, gh)
	Replay(context.Background(), svc, []ToolCall{call("add_transactions", augustAdd)})
	gh.conflict = true
	_, err := st.Flush(context.Background(), st.base, "octocat")
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict || e.Details.(map[string]any)["failed_path"] != augustPath {
		t.Fatalf("want a conflict naming the path, got %v", err)
	}
}

func TestRevertRestoresThePreviousBlobOrRemovesACreatedFile(t *testing.T) {
	original := doc(tx("t1", "2026-08-01", "900", idRent))
	gh := newFakeGitHub(map[string]string{augustPath: original})
	svc, st := stagedService(t, gh)
	Replay(context.Background(), svc, []ToolCall{
		call("add_transactions", augustAdd),
		call("add_transactions", `{"transactions":[{"id":"s1","date":"2026-09-02","description":"SEPT","amount":5,"account":"A","category":"`+idRent+`"}],"coverage":[{"account":"A","from":"2026-09-01","to":"2026-09-30","imported_at":"2026-10-01"}]}`),
	})
	receipts, err := st.Flush(context.Background(), st.base, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	base := st.base.(*ContentsClient)

	restored, err := Revert(context.Background(), base, base, receipts[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original || string(gh.files[augustPath]) != original || !strings.HasPrefix(gh.lastMsg, "revert: feat(actuals): add 1 transaction to 2026-08") {
		t.Errorf("august was not restored: %q\n%s", gh.lastMsg, gh.files[augustPath])
	}

	if _, err := Revert(context.Background(), base, base, receipts[1]); err != nil {
		t.Fatal(err)
	}
	if _, ok := gh.files["data/actuals/2026-09.json"]; ok || len(gh.deletes) != 1 {
		t.Errorf("the created month must be removed again: %v", gh.deletes)
	}

	_, err = Revert(context.Background(), base, base, receipts[0])
	if e, ok := err.(*Error); !ok || e.Code != CodeConflict {
		t.Errorf("a second revert of the same receipt must be a conflict, got %v", err)
	}
}

func TestPublishDispatchesByPath(t *testing.T) {
	svc := &Service{}
	if !svc.isActualsPath("data/actuals/2026-08.json") || svc.isActualsPath("data/budget.json") || svc.isActualsPath("data/actuals/x/2026-08.json") {
		t.Error("actuals paths are the month files directly under the prefix")
	}
	if monthFileKey("data/actuals/2026-08.json") != "2026-08" {
		t.Error("month of path")
	}
}
