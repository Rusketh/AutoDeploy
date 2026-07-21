package resolve

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func seqSetup(t *testing.T) (*storage.DB, *model.ImageRepo, *model.SequenceRepo, *model.TaskRepo, *Resolver) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	images := model.NewImageRepo(db)
	sequences := model.NewSequenceRepo(db)
	tasks := model.NewTaskRepo(db)
	r := New(images, isos, unattend).WithSequences(sequences, tasks)
	return db, images, sequences, tasks, r
}

// A linked sequence flattens nested subsequences inline, in order.
func TestResolveSequenceFlattensNested(t *testing.T) {
	ctx := context.Background()
	_, images, sequences, tasks, r := seqSetup(t)

	tk, _ := tasks.Create(ctx, model.Task{Name: "Script A", PayloadBody: "echo a"})
	tid := tk.ID

	inner, _ := sequences.Create(ctx, model.Sequence{
		Name: "inner",
		Steps: []model.SequenceStep{
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinGPUpdate, OrderValue: 10},
			{Kind: model.SeqStepTask, TaskID: &tid, OrderValue: 20},
		},
	})
	innerID := inner.ID
	outer, _ := sequences.Create(ctx, model.Sequence{
		Name: "outer",
		Steps: []model.SequenceStep{
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinDomainJoin, OrderValue: 10},
			{Kind: model.SeqStepSubsequence, ChildSequenceID: &innerID, OrderValue: 20},
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinSoftware, OrderValue: 30},
		},
	})
	outerID := outer.ID

	img, _ := images.Create(ctx, model.Image{Name: "img", SequenceID: &outerID})

	got, err := r.Resolve(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Expected flattened order: domainjoin, gpupdate, task(Script A), software.
	want := []string{"domainjoin", "gpupdate", "task:Script A", "software"}
	if len(got.Sequence) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(got.Sequence), len(want), got.Sequence)
	}
	for i, step := range got.Sequence {
		label := step.BuiltinAction
		if step.Kind == model.SeqStepTask {
			label = "task:" + step.Task.Name
		}
		if label != want[i] {
			t.Errorf("step %d = %q, want %q", i, label, want[i])
		}
	}
}

// An image with no linked sequence resolves to the synthetic default.
func TestResolveDefaultSequence(t *testing.T) {
	ctx := context.Background()
	_, images, _, _, r := seqSetup(t)
	img, _ := images.Create(ctx, model.Image{Name: "plain"})

	got, err := r.Resolve(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sequence) != 2 ||
		got.Sequence[0].BuiltinAction != model.BuiltinDomainJoin ||
		got.Sequence[1].BuiltinAction != model.BuiltinSoftware {
		t.Fatalf("default sequence = %+v", got.Sequence)
	}
}

// sequence_id resolves nearest-wins up the parent chain.
func TestResolveSequenceNearestWins(t *testing.T) {
	ctx := context.Background()
	_, images, sequences, _, r := seqSetup(t)

	base, _ := sequences.Create(ctx, model.Sequence{
		Name:  "base",
		Steps: []model.SequenceStep{{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinSoftware, OrderValue: 10}},
	})
	over, _ := sequences.Create(ctx, model.Sequence{
		Name:  "override",
		Steps: []model.SequenceStep{{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinReboot, OrderValue: 10}},
	})
	baseID := base.ID
	overID := over.ID

	root, _ := images.Create(ctx, model.Image{Name: "root", SequenceID: &baseID})
	rootID := root.ID
	child, _ := images.Create(ctx, model.Image{Name: "child", ParentID: &rootID, SequenceID: &overID})

	got, err := r.Resolve(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sequence) != 1 || got.Sequence[0].BuiltinAction != model.BuiltinReboot {
		t.Fatalf("nearest-wins failed: %+v", got.Sequence)
	}

	// The parent still resolves to the base sequence.
	gotRoot, _ := r.Resolve(ctx, root.ID)
	if len(gotRoot.Sequence) != 1 || gotRoot.Sequence[0].BuiltinAction != model.BuiltinSoftware {
		t.Fatalf("root sequence = %+v", gotRoot.Sequence)
	}
}

// A stored cycle (corrupt data that bypassed the repo's save-time guard) makes
// resolution hard-fail with ErrCycle rather than loop forever.
func TestResolveSequenceCycleHardFails(t *testing.T) {
	ctx := context.Background()
	db, images, sequences, _, r := seqSetup(t)

	a, _ := sequences.Create(ctx, model.Sequence{Name: "A"})
	b, _ := sequences.Create(ctx, model.Sequence{Name: "B"})
	aID, bID := a.ID, b.ID

	// The repo forbids building a cycle, so inject the edges directly to
	// simulate corrupt rows: A→B and B→A.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sequence_step (sequence_id, order_value, kind, child_sequence_id)
		 VALUES (?, 10, 'subsequence', ?), (?, 10, 'subsequence', ?)`,
		aID, bID, bID, aID); err != nil {
		t.Fatal(err)
	}

	img, _ := images.Create(ctx, model.Image{Name: "img", SequenceID: &aID})
	if _, err := r.Resolve(ctx, img.ID); !errors.Is(err, model.ErrCycle) {
		t.Fatalf("cyclic resolve: want ErrCycle, got %v", err)
	}
}
