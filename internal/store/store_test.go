package store

import (
	"os"
	"path/filepath"
	"testing"

	"arcproof/internal/domain"
)

func TestPersistenceAndAudit(t *testing.T) {
	dir := t.TempDir()
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.Update("tester", "create", "rule", "r-1", func(s *domain.State) error {
		s.Rules["r-1"] = domain.RuleSet{ID: "r-1", Name: "测试", Status: domain.RuleDraft, Version: 1}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	s, err := repo.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rules) != 1 || len(s.Audit) != 1 {
		t.Fatalf("恢复状态异常: %+v", s)
	}
}

func TestIncompleteJournalTailIsTruncated(t *testing.T) {
	dir := t.TempDir()
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, journalName), os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{broken")
	_ = f.Close()
	repo, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	degraded, reason := repo.Degraded()
	if !degraded || reason == "" {
		t.Fatal("应报告降级")
	}
}
