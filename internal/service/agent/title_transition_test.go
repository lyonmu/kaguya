package agent

import (
	"context"
	"errors"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
	"go.uber.org/zap"
)

func setupTitleTransitions(t *testing.T) (context.Context, *ent.Client, *TitleQueue, <-chan dtochat.TitleEvent) {
	t.Helper()
	ctx, client := setupChatTest(t)
	DefaultTitleQueue.Close()
	waitTitleQueueClosed(t, DefaultTitleQueue)
	oldPublisher := DefaultTitlePublisher
	DefaultTitlePublisher = NewTitlePublisher()
	subscription, unsubscribe, err := DefaultTitlePublisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	queue := NewTitleQueue(ctx, client, zap.NewNop())
	t.Cleanup(func() {
		queue.Close()
		waitTitleQueueClosed(t, queue)
		unsubscribe()
		DefaultTitlePublisher.Close()
		DefaultTitlePublisher = oldPublisher
	})
	return ctx, client, queue, subscription.Events
}

func requireTitleJobEvent(t *testing.T, events <-chan dtochat.TitleEvent, id string, status kaguyatitlejob.Status, revision int64) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != dtochat.TitleJobEventUpdated || event.ConversationID != id || event.JobStatus != string(status) || event.JobRevision == nil || *event.JobRevision != revision || event.Title != "" {
			t.Fatalf("event=%+v, want status=%s revision=%d", event, status, revision)
		}
	default:
		t.Fatal("missing committed title job event")
	}
}

func requireNoTitleEvent(t *testing.T, events <-chan dtochat.TitleEvent) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected title event=%+v", event)
	default:
	}
}

func TestTitleJobTransitionsUseCommittedIndependentRevision(t *testing.T) {
	ctx, client, queue, events := setupTitleTransitions(t)
	createTitleConversation(t, ctx, client, "transitions")
	if err := queue.Enqueue(ctx, "transitions", "first question", ""); err != nil {
		t.Fatal(err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusPending, 0)
	if err := queue.Enqueue(ctx, "transitions", "ignored replacement", ""); err != nil {
		t.Fatal(err)
	}
	requireNoTitleEvent(t, events)
	job, err := queue.claim()
	if err != nil || job == nil || job.Question != "first question" || job.Revision != 1 || job.AttemptCount != 1 {
		t.Fatalf("claimed=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusRunning, 1)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	queue.finish(canceled, job, kaguyatitlejob.StatusFailed, "generation_failed")
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusFailed, 2)
	queue.finish(ctx, job, kaguyatitlejob.StatusFailed, "late_failure")
	requireNoTitleEvent(t, events)
	if err := queue.Enqueue(ctx, "transitions", "retry question", ""); err != nil {
		t.Fatal(err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusPending, 3)
	job, err = queue.claim()
	if err != nil || job == nil || job.Revision != 4 {
		t.Fatalf("retry claim=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusRunning, 4)
	queue.revertToPending(canceled, job)
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusPending, 5)
	job, err = queue.claim()
	if err != nil || job == nil || job.Revision != 6 {
		t.Fatalf("restart claim=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusRunning, 6)
	revision, updated, err := queue.commitSuccess(ctx, job, "已保存标题")
	if err != nil || !updated || revision != 1 {
		t.Fatalf("commit=%d/%v err=%v", revision, updated, err)
	}
	requireTitleJobEvent(t, events, "transitions", kaguyatitlejob.StatusSucceeded, 7)
	stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
	if err != nil || stored.Revision != 7 || stored.Status != kaguyatitlejob.StatusSucceeded {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if err := queue.Enqueue(ctx, "transitions", "no resurrection", ""); err != nil {
		t.Fatal(err)
	}
	queue.finish(ctx, job, kaguyatitlejob.StatusFailed, "late_failure")
	requireNoTitleEvent(t, events)
}

func TestTitleJobCommitFailureRollsBackWithoutEvent(t *testing.T) {
	ctx, client, queue, events := setupTitleTransitions(t)
	createTitleConversation(t, ctx, client, "rollback")
	if err := queue.Enqueue(ctx, "rollback", "question", ""); err != nil {
		t.Fatal(err)
	}
	requireTitleJobEvent(t, events, "rollback", kaguyatitlejob.StatusPending, 0)
	job, err := queue.claim()
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "rollback", kaguyatitlejob.StatusRunning, 1)
	fault := errors.New("injected title persistence failure")
	client.KaguyaConversation.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if _, present := mutation.(*ent.KaguyaConversationMutation).Title(); present {
				return nil, fault
			}
			return next.Mutate(ctx, mutation)
		})
	})
	if _, updated, err := queue.commitSuccess(ctx, job, "不可发布标题"); !errors.Is(err, fault) || updated {
		t.Fatalf("commit updated=%v err=%v", updated, err)
	}
	requireNoTitleEvent(t, events)
	stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
	if err != nil || stored.Status != kaguyatitlejob.StatusRunning || stored.Revision != 1 {
		t.Fatalf("rolled back job=%+v err=%v", stored, err)
	}
	conversation, err := client.KaguyaConversation.Get(ctx, "rollback")
	if err != nil || conversation.Title != defaultConversationTitle || conversation.TitleRevision != 0 {
		t.Fatalf("rolled back title=%+v err=%v", conversation, err)
	}
}

func TestTitleJobCancelDoesNotAllowLateSuccessOrFailure(t *testing.T) {
	for _, deleteConversation := range []bool{false, true} {
		t.Run(map[bool]string{false: "rename", true: "delete"}[deleteConversation], func(t *testing.T) {
			ctx, client, queue, events := setupTitleTransitions(t)
			createTitleConversation(t, ctx, client, "cancel")
			if err := queue.Enqueue(ctx, "cancel", "question", ""); err != nil {
				t.Fatal(err)
			}
			requireTitleJobEvent(t, events, "cancel", kaguyatitlejob.StatusPending, 0)
			job, err := queue.claim()
			if err != nil || job == nil {
				t.Fatalf("claim=%+v err=%v", job, err)
			}
			requireTitleJobEvent(t, events, "cancel", kaguyatitlejob.StatusRunning, 1)
			if deleteConversation {
				if err := (&AgentSvc{}).ConversationDelete(ctx, "cancel", false); err != nil {
					t.Fatal(err)
				}
			} else {
				// 仍是默认文本也应撤销在途任务；仅检查标题文本不能防复活。
				title := defaultConversationTitle
				if _, err := (&AgentSvc{}).ConversationUpdate(ctx, "cancel", &dtochat.ConversationUpdateReq{Title: &title}); err != nil {
					t.Fatal(err)
				}
			}
			requireTitleJobEvent(t, events, "cancel", kaguyatitlejob.StatusCanceled, 2)
			if !deleteConversation {
				event := <-events
				if event.Type != dtochat.TitleEventUpdated || event.Revision != 1 {
					t.Fatalf("rename event=%+v", event)
				}
			}
			if _, updated, err := queue.commitSuccess(ctx, job, "迟到标题"); err != nil || updated {
				t.Fatalf("late commit updated=%v err=%v", updated, err)
			}
			queue.finish(ctx, job, kaguyatitlejob.StatusFailed, "late_failure")
			queue.revertToPending(ctx, job)
			requireNoTitleEvent(t, events)
			stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
			if err != nil || stored.Status != kaguyatitlejob.StatusCanceled || stored.Revision != 2 {
				t.Fatalf("canceled job changed=%+v err=%v", stored, err)
			}
		})
	}
}

func TestTitleJobFinishPersistenceFailureDoesNotPublish(t *testing.T) {
	ctx, client, queue, events := setupTitleTransitions(t)
	createTitleConversation(t, ctx, client, "finish-fault")
	if err := queue.Enqueue(ctx, "finish-fault", "question", ""); err != nil {
		t.Fatal(err)
	}
	requireTitleJobEvent(t, events, "finish-fault", kaguyatitlejob.StatusPending, 0)
	job, err := queue.claim()
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "finish-fault", kaguyatitlejob.StatusRunning, 1)
	client.KaguyaTitleJob.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			return nil, errors.New("injected job write failure")
		})
	})
	queue.finish(ctx, job, kaguyatitlejob.StatusFailed, "generation_failed")
	requireNoTitleEvent(t, events)
	stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
	if err != nil || stored.Status != kaguyatitlejob.StatusRunning || stored.Revision != 1 {
		t.Fatalf("uncommitted failure changed job=%+v err=%v", stored, err)
	}
}

func TestTitleJobShutdownDuringLookupRestoresPending(t *testing.T) {
	ctx, client, queue, events := setupTitleTransitions(t)
	createTitleConversation(t, ctx, client, "lookup-shutdown")
	if err := queue.Enqueue(ctx, "lookup-shutdown", "question", ""); err != nil {
		t.Fatal(err)
	}
	requireTitleJobEvent(t, events, "lookup-shutdown", kaguyatitlejob.StatusPending, 0)
	job, err := queue.claim()
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	requireTitleJobEvent(t, events, "lookup-shutdown", kaguyatitlejob.StatusRunning, 1)
	client.KaguyaConversation.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			value, err := next.Query(ctx, query)
			queue.cancel()
			return value, err
		})
	}))
	queue.execute(queue.ctx, job)
	requireTitleJobEvent(t, events, "lookup-shutdown", kaguyatitlejob.StatusPending, 2)
	stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
	if err != nil || stored.Status != kaguyatitlejob.StatusPending || stored.ErrorCode != "" || stored.Revision != 2 {
		t.Fatalf("shutdown was recorded as a generation failure: %+v err=%v", stored, err)
	}
}
