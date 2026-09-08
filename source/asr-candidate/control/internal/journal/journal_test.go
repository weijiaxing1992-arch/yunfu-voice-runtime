package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJournalRecoveryLockAndFlush 验证残缺尾行恢复、独占写锁和关闭时同步落盘。
func TestJournalRecoveryLockAndFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"kind\":\"old\"}\n{\"partial\":"), 0600); err != nil {
		t.Fatal(err)
	}
	j, e := Open(path, 64)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(path, 64); e == nil {
		second.Close()
		t.Fatal("accepted second writer")
	}
	if !j.Record(Event{Kind: "new"}) {
		t.Fatal("journal rejected event")
	}
	j.Close()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "partial") || !strings.Contains(string(data), "new") {
		t.Fatal(string(data))
	}
	if j.Synced.Load() != 1 {
		t.Fatal("event was not fsynced")
	}
}

// TestJournalRejectsCorruptCompleteRecord 防止把完整的损坏记录误当可丢弃尾部并静默截断。
func TestJournalRejectsCorruptCompleteRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	_ = os.WriteFile(path, []byte("broken\n"), 0600)
	if j, e := Open(path, 64); e == nil {
		j.Close()
		t.Fatal("accepted corrupt journal")
	}
}
