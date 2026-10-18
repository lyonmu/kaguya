package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
)

func TestTitleSnapshotsBatchReadDoesNotCreateJobsOrReadBody(t *testing.T) {
	ctx, client, _, events := setupTitleTransitions(t)
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = fmt.Sprintf("snapshot-%03d", i)
		createTitleConversation(t, ctx, client, ids[i])
	}
	if err := client.KaguyaConversation.UpdateOneID(ids[0]).SetTitle("版本化标题").SetTitleRevision(3).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaTitleJob.Create().SetConversationID(ids[0]).SetStatus(kaguyatitlejob.StatusFailed).
		SetRevision(7).SetEnqueueOrder(1).SetQuestion("SNAPSHOT_PRIVATE_BODY").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaConversation.UpdateOneID(ids[1]).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	client.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			queries.Add(1)
			return next.Query(ctx, query)
		})
	}))
	resp, err := (&AgentSvc{}).ConversationTitleSnapshots(ctx, &dtochat.TitleSnapshotsReq{IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if queries.Load() != 2 || len(resp.Items) != 199 || len(resp.MissingIDs) != 1 || resp.MissingIDs[0] != ids[1] {
		t.Fatalf("queries=%d snapshot=%+v", queries.Load(), resp)
	}
	first := resp.Items[0]
	if first.ID != ids[0] || first.Title != "版本化标题" || first.TitleRevision != 3 || first.JobStatus == nil || *first.JobStatus != "failed" || first.JobRevision != 7 {
		t.Fatalf("snapshot=%+v", first)
	}
	if second := resp.Items[1]; second.ID != ids[2] || second.JobStatus != nil || second.JobRevision != 0 {
		t.Fatalf("no-job snapshot=%+v", second)
	}
	encoded, err := json.Marshal(resp)
	if err != nil || strings.Contains(string(encoded), "SNAPSHOT_PRIVATE_BODY") {
		t.Fatalf("snapshot exposed body or failed encoding: %s err=%v", encoded, err)
	}
	if count, err := client.KaguyaTitleJob.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("snapshot created jobs: count=%d err=%v", count, err)
	}
	requireNoTitleEvent(t, events)
}

func TestTitleSnapshotsRejectInvalidRequestsBeforeQuery(t *testing.T) {
	ctx, client, _, _ := setupTitleTransitions(t)
	var queries atomic.Int32
	client.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			queries.Add(1)
			return next.Query(ctx, query)
		})
	}))
	for _, req := range []*dtochat.TitleSnapshotsReq{nil, {}, {IDs: []string{}}, {IDs: []string{""}}, {IDs: []string{"  "}}, {IDs: []string{"nul\x00id"}}, {IDs: []string{string([]byte{0xff})}}, {IDs: []string{strings.Repeat("中", 65)}}, {IDs: make([]string, 201)}} {
		if resp, err := (&AgentSvc{}).ConversationTitleSnapshots(ctx, req); !errors.Is(err, ErrConversationUpdate) || resp != nil {
			t.Fatalf("invalid request returned snapshot=%+v err=%v", resp, err)
		}
	}
	if queries.Load() != 0 {
		t.Fatalf("invalid requests queried database %d times", queries.Load())
	}
}

func TestTitleSnapshotsKeepOneTransactionAcrossBothReads(t *testing.T) {
	ctx, client, _, _ := setupTitleTransitions(t)
	createTitleConversation(t, ctx, client, "atomic-snapshot")
	job, err := client.KaguyaTitleJob.Create().SetConversationID("atomic-snapshot").SetQuestion("question").SetStatus(kaguyatitlejob.StatusRunning).
		SetRevision(1).SetEnqueueOrder(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan error, 1)
	client.KaguyaConversation.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			value, err := next.Query(ctx, query)
			if err != nil {
				return value, err
			}
			go func() {
				tx, err := client.Tx(ctx)
				if err != nil {
					writerDone <- err
					return
				}
				defer func() { _ = tx.Rollback() }()
				if err = tx.Client().KaguyaConversation.UpdateOneID("atomic-snapshot").SetTitle("新标题").SetTitleRevision(1).Exec(ctx); err == nil {
					err = tx.Client().KaguyaTitleJob.UpdateOneID(job.ID).SetStatus(kaguyatitlejob.StatusSucceeded).SetRevision(2).Exec(ctx)
				}
				if err == nil {
					err = tx.Commit()
				}
				writerDone <- err
			}()
			// 既有单连接测试库中，读取事务必须持有连接直到第二次读取完。
			select {
			case err := <-writerDone:
				writerDone <- err
				return nil, errors.New("writer committed between snapshot reads")
			case <-time.After(50 * time.Millisecond):
			}
			return value, nil
		})
	}))
	resp, readErr := (&AgentSvc{}).ConversationTitleSnapshots(ctx, &dtochat.TitleSnapshotsReq{IDs: []string{"atomic-snapshot"}})
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("writer remained blocked after the snapshot transaction ended")
	}
	if readErr != nil || resp == nil || len(resp.Items) != 1 {
		t.Fatalf("snapshot=%+v err=%v", resp, readErr)
	}
	item := resp.Items[0]
	if item.Title != defaultConversationTitle || item.TitleRevision != 0 || item.JobStatus == nil || *item.JobStatus != "running" || item.JobRevision != 1 {
		t.Fatalf("mixed committed versions=%+v", item)
	}
}
