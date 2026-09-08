package server

import (
	"fmt"
	"io"
)

// asrMetrics复用实际管理器快照；不遍历Call、不输出全文/UUID/socket/token标签。
// starting与未知清理都占槽，Active仅表示资源有效，不代表模型识别成功。
func (s *Server) asrMetrics(w io.Writer) {
	a := s.ASRSnapshot()
	enabled, stopping := 0, 0
	if a.Enabled {
		enabled = 1
	}
	if a.Stopping {
		stopping = 1
	}
	fmt.Fprintf(w, "rustswitch_asr_enabled %d\nrustswitch_asr_stopping %d\nrustswitch_asr_max_streams %d\nrustswitch_asr_slots %d\nrustswitch_asr_starting %d\nrustswitch_asr_active %d\nrustswitch_asr_cleanup_pending %d\nrustswitch_asr_rejected_capacity_total %d\nrustswitch_asr_rejected_uuid_total %d\n", enabled, stopping, a.MaxStreams, a.Slots, a.Starting, a.Active, a.CleanupPending, a.RejectedCapacity, a.RejectedUUID)
}
