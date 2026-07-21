package model

import (
	"context"
	"errors"
	"testing"
)

func TestTaskCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	repo := NewTaskRepo(openTestDB(t))

	// Create with defaults: shell defaults to powershell, success codes to [0].
	tk, err := repo.Create(ctx, Task{Name: "Hello", PayloadBody: "Write-Output hi"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.PayloadShell != TaskShellPowerShell || tk.SuccessCodesJSON != "[0]" {
		t.Fatalf("defaults wrong: %+v", tk)
	}

	// Unknown shell rejected.
	if _, err := repo.Create(ctx, Task{Name: "Bad", PayloadShell: "bash"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad shell: want ErrValidation, got %v", err)
	}
	// Unknown filter type rejected.
	if _, err := repo.Create(ctx, Task{Name: "Bad2", FilterType: "reg"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bad filter: want ErrValidation, got %v", err)
	}
	// A filter type with no value is rejected (would never match).
	if _, err := repo.Create(ctx, Task{Name: "Bad3", FilterType: TaskFilterOS}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty filter value: want ErrValidation, got %v", err)
	}

	// A valid OS filter round-trips.
	osT, err := repo.Create(ctx, Task{Name: "Win11 only", FilterType: TaskFilterOS, FilterValue: "Windows 11"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, osT.ID)
	if err != nil || got.FilterType != TaskFilterOS || got.FilterValue != "Windows 11" {
		t.Fatalf("filter round-trip: %+v err=%v", got, err)
	}
}

func TestTaskRefCountBlocksDelete(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks := NewTaskRepo(db)
	seqs := NewSequenceRepo(db)

	tk, err := tasks.Create(ctx, Task{Name: "Run me", PayloadBody: "echo x"})
	if err != nil {
		t.Fatal(err)
	}
	tid := tk.ID
	if _, err := seqs.Create(ctx, Sequence{
		Name:  "Uses task",
		Steps: []SequenceStep{{Kind: SeqStepTask, TaskID: &tid, OrderValue: 10}},
	}); err != nil {
		t.Fatal(err)
	}
	// RefCount sees the reference; Delete refuses.
	if n, _ := tasks.RefCount(ctx, tid); n != 1 {
		t.Errorf("RefCount = %d, want 1", n)
	}
	if err := tasks.Delete(ctx, tid); !errors.Is(err, ErrInUse) {
		t.Errorf("Delete referenced task: want ErrInUse, got %v", err)
	}
}
