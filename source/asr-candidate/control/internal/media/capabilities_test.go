package media

import "testing"

// TestCapabilitySnapshotCannotReuseDeadGeneration 覆盖故障、无身份与旧能力残留，禁止管理目录在重启窗口继续绿灯。
func TestCapabilitySnapshotCannotReuseDeadGeneration(t *testing.T) {
	w := Worker{Config: WorkerConfig{WorkerID: 0}, capabilities: []string{"processed_g711_v1"}}
	w.Healthy.Store(true)
	if proof := w.CapabilitySnapshot(); proof.Healthy || len(proof.Capabilities) != 0 {
		t.Fatal(proof)
	}
	w.Generation.Store(1)
	w.PID.Store(123)
	if proof := w.CapabilitySnapshot(); !proof.Healthy || len(proof.Capabilities) != 1 {
		t.Fatal(proof)
	}
	w.Healthy.Store(false)
	if proof := w.CapabilitySnapshot(); proof.Healthy || len(proof.Capabilities) != 0 {
		t.Fatal(proof)
	}
	// 后继旧 worker 没有新能力，即使健康也不能复用上一代的列表。
	w.submitMu.Lock()
	w.Generation.Store(2)
	w.PID.Store(124)
	w.capabilities = nil
	w.Healthy.Store(true)
	w.submitMu.Unlock()
	if proof := w.CapabilitySnapshot(); !proof.Healthy || proof.Generation != 2 || len(proof.Capabilities) != 0 {
		t.Fatal(proof)
	}
}
