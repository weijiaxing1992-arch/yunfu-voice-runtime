package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"rustswitch/control/internal/asr/wire"
)

func evidenceEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		result = append(result, event)
	}
	return result
}

func TestEvidenceRejectionRetainsActualCheckAndOriginalDeadline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "one")
	e, err := newEvidence(dir, nil, &evidenceBudget{maximum: 65536}, 32768)
	if err != nil {
		t.Fatal(err)
	}
	defer e.closeFiles()
	p := prepared(t, 8000)
	m := audioMessage(t, fixtureAudio(2, 8000))
	checked := fixtureNow + wire.LifetimeNS
	_, failure := p.accept(m, checked)
	if failure == nil {
		t.Fatal("到期边界没有拒绝")
	}
	if err := e.rejected(m, checked, failure); err != nil {
		t.Fatal(err)
	}
	events := evidenceEvents(t, dir)
	if len(events) != 1 || events[0]["stage"] != "consume-rejected" || events[0]["checked_clock_ns"] != strconv.FormatUint(checked, 10) || events[0]["lower_ns"] != strconv.FormatUint(fixtureNow, 10) || events[0]["expires_ns"] != strconv.FormatUint(fixtureNow+wire.LifetimeNS, 10) || events[0]["error"] != failure.Error() {
		t.Fatal("拒绝日志把检查点或原期限替换为日志时间", events)
	}
	if e.pcmBytes != 0 || p.samples != 0 {
		t.Fatal("拒绝帧被计为已消费")
	}
}

func TestEvidenceFinalAttemptIsSeparateFromAcceptedPrefix(t *testing.T) {
	for _, accepted := range []int{0, 7, 32} {
		t.Run(strconv.Itoa(accepted), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "one")
			e, err := newEvidence(dir, nil, &evidenceBudget{maximum: 65536}, 32768)
			if err != nil {
				t.Fatal(err)
			}
			defer e.closeFiles()
			raw := bytes.Repeat([]byte{0x42}, 32)
			injected := errors.New("offline simulated peer closed")
			err = e.writeResponse(raw, true, func(b []byte) error {
				if !bytes.Equal(b, raw) {
					t.Fatal("准备的原文改变")
				}
				if err := e.writeFile(e.out, b[:accepted]); err != nil {
					return err
				}
				e.outBytes += uint64(accepted)
				if accepted < len(raw) {
					return injected
				}
				return nil
			})
			if (err == nil) != (accepted == len(raw)) {
				t.Fatal("写错误丢失", err)
			}
			attempt, _ := os.ReadFile(filepath.Join(dir, "final-attempted.bin"))
			actual, _ := os.ReadFile(filepath.Join(dir, "outbound.bin"))
			if !bytes.Equal(attempt, raw) || !bytes.Equal(actual, raw[:accepted]) {
				t.Fatal("尝试内容被混入实际写出字节")
			}
			events := evidenceEvents(t, dir)
			if len(events) != 2 || events[0]["stage"] != "final-write-attempt" || events[1]["stage"] != "final-write-result" || events[1]["accepted_bytes"] != float64(accepted) {
				t.Fatal(events)
			}
			first, _ := strconv.ParseUint(events[0]["clock_ns"].(string), 10, 64)
			last, _ := strconv.ParseUint(events[1]["clock_ns"].(string), 10, 64)
			if first == 0 || last < first {
				t.Fatal("尝试/结果时钟无效")
			}
		})
	}
}
