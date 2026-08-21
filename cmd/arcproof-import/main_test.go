package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"arcproof/internal/app"
	"arcproof/internal/sample"
	"arcproof/internal/store"
)

func TestImportDryRunAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "joints.ndjson")
	input := app.CreateRequirementInput{Reference: "BATCH-001", Variables: sample.Variables()}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = run([]string{"-data-dir", dir, "-file", file, "-actor", "tester", "-batch-key", "batch-1", "-dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	repo, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Close()
	if len(state.Requirements) != 0 {
		t.Fatal("dry-run 不应落盘")
	}
	output.Reset()
	if err = run([]string{"-data-dir", dir, "-file", file, "-actor", "tester", "-batch-key", "batch-1"}, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err = run([]string{"-data-dir", dir, "-file", file, "-actor", "tester", "-batch-key", "batch-1"}, &output); err != nil {
		t.Fatal(err)
	}
	repo, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	state, err = repo.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Requirements) != 1 {
		t.Fatalf("重复批次产生了 %d 条记录", len(state.Requirements))
	}
}

func TestImportRejectsUnknownFieldAtomically(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "bad.ndjson")
	valid, _ := json.Marshal(app.CreateRequirementInput{Reference: "OK", Variables: sample.Variables()})
	bad := append([]byte(nil), valid[:len(valid)-1]...)
	bad = append(bad, []byte(`,"unknown":true}`)...)
	content := append(append(valid, '\n'), bad...)
	content = append(content, '\n')
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-data-dir", dir, "-file", file, "-actor", "tester"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("未知字段应使整批失败")
	}
	repo, openErr := store.Open(dir)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer repo.Close()
	state, _ := repo.Snapshot()
	if len(state.Requirements) != 0 {
		t.Fatal("非法批次不应部分落盘")
	}
}
