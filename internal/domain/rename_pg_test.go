package domain_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dustywusty/yak/internal/domain"
	"github.com/dustywusty/yak/internal/events"
	"github.com/dustywusty/yak/internal/postgres"
)

func TestRenamePreservesEventHistory(t *testing.T) {
	x := setupP2(t)
	ctx := context.Background()
	tk := x.task("work", "before rename")
	// Simulate an event and server label written by the previous release.
	if _, err := x.svc.DB.Exec(ctx, `UPDATE events SET payload=replace(payload::text,'yak.task.','abby.task.')::jsonb, facets=replace(facets::text,'yak.task.','abby.task.')::jsonb WHERE board_id=$1`, x.board); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.DB.Exec(ctx, `INSERT INTO message_labels(message_id,key,value) SELECT root_message_id,'abby.task.brief_revision','1' FROM threads WHERE id=$1`, tk.ThreadID); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := x.svc.DB.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('id',id,'position',position,'payload',payload,'facets',facets) ORDER BY position) FROM events WHERE board_id=$1`, x.board).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte("abby.task.state")) {
		t.Fatal("fixture has no old label")
	}
	page, err := x.svc.ReadEvents(ctx, x.admin, x.board, domain.ReadEventsRequest{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	cursor := page.NextCursor
	beforeHead := x.head()
	if _, err := postgres.Migrate(ctx, x.svc.DB.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.claim(x.admin, tk.ID, 60); err != nil {
		t.Fatal(err)
	}
	continued, err := x.svc.ReadEvents(ctx, x.admin, x.board, domain.ReadEventsRequest{Cursor: cursor, Limit: 200})
	if err != nil || len(continued.Events) == 0 {
		t.Fatalf("old cursor did not continue: %v", err)
	}
	old, err := x.svc.ReadEvents(ctx, x.admin, x.board, domain.ReadEventsRequest{Limit: 200, Filter: events.Filter{Labels: map[string][]string{"abby.task.state": {"ready"}}}})
	if err != nil || len(old.Events) == 0 {
		t.Fatalf("old label history inaccessible: %v", err)
	}
	var found bool
	for _, e := range old.Events {
		if strings.Contains(string(e.Payload), "abby.task.state") {
			found = true
		}
	}
	if !found {
		t.Fatal("old event payload label was rewritten")
	}
	var after []byte
	if err := x.svc.DB.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('id',id,'position',position,'payload',payload,'facets',facets) ORDER BY position) FROM events WHERE board_id=$1 AND position<=$2`, x.board, beforeHead).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stored historical event identity, payload or facets changed")
	}
	var root string
	if err := x.svc.DB.QueryRow(ctx, `SELECT root_message_id FROM threads WHERE id=$1`, tk.ThreadID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	value := "new"
	msg, err := x.svc.MutateLabels(ctx, x.admin, x.board, root, nil, domain.LabelsRequest{Operations: []domain.LabelOp{{Op: "set", Key: "area", Value: &value}}})
	if err != nil || msg.Labels["abby.task.brief_revision"] != "1" {
		t.Fatalf("client label update lost historical server label: %v", err)
	}
	for _, key := range []string{"abby.task.brief_revision", "yak.task.state"} {
		_, err := x.svc.MutateLabels(ctx, x.admin, x.board, root, nil, domain.LabelsRequest{Operations: []domain.LabelOp{{Op: "set", Key: key, Value: &value}}})
		if code(err) != "validation_failed" {
			t.Fatalf("reserved label write %s: %v", key, err)
		}
	}
	threads, err := x.svc.ListThreads(ctx, x.admin, x.board, events.Filter{Labels: map[string][]string{"yak.task.state": {"claimed"}}}, 0, 50)
	if err != nil || len(threads.Threads) != 1 {
		t.Fatalf("current task label missing: %v", err)
	}
}
