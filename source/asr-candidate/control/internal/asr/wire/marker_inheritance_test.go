package wire

import "testing"

// Rust拒收与观察溢出保留原始来源事实；逐类限定，不能放开任意discarded字段。
func TestMarkerActualExpiredAndObservationFailureInheritance(t *testing.T) {
	for _, kind := range []uint8{5, 6, 9} {
		m := testMarker()
		m.RXKind, m.Flags, m.Boundary, m.DiscardedPackets = kind, 0, 0, 1
		m.Reason = 10
		if kind == 5 {
			d := U64(160)
			m.KnownDurationTicks = &d
		}
		if kind == 9 {
			m.Reason = 8
			m.Flags, m.Boundary, m.DiscardedPackets = 16, 2, 23
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("真实继承kind%d被拒绝: %v", kind, err)
		}
		if kind == 9 {
			m.Boundary, m.Reason = 6, 9
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			m.Flags |= 4
			if m.Validate() == nil {
				t.Fatal("结束源不能伪造已知包序号")
			}
			continue
		}
		for _, change := range []func(*Marker){func(m *Marker) { m.DiscardedPackets = 2 }, func(m *Marker) { m.Reason = 8 }, func(m *Marker) { d := U64(80); m.KnownDurationTicks = &d }, func(m *Marker) { m.CNApplied = true }} {
			bad := m
			change(&bad)
			if bad.Validate() == nil {
				t.Fatalf("kind%d非法过期矩阵被接受", kind)
			}
		}
	}
}

func TestMarkerTerminalInheritanceRejectsMixedFacts(t *testing.T) {
	for _, boundary := range []uint8{0, 1, 2, 3, 4, 5, 6} {
		for _, count := range []U64{0, 1, 23} {
			for _, duration := range []U64{0, 160} {
				for _, cn := range []bool{false, true} {
					m := testMarker()
					m.RXKind, m.Reason, m.Boundary, m.DiscardedPackets, m.CNApplied = 9, 8, boundary, count, cn
					m.Flags = 0
					if boundary != 0 {
						m.Flags = 16
					}
					if duration != 0 {
						d := duration
						m.KnownDurationTicks = &d
					}
					valid := false
					switch boundary {
					case 0:
						valid = !cn && (count == 0 || count == 1) || cn && count == 0 && duration == 0
					case 1:
						valid = count == 0 && duration == 0 && !cn
					case 2, 3, 4, 6:
						valid = duration == 0 && !cn
					case 5:
						valid = duration == 160 && count == 0 && !cn
					}
					if (m.Validate() == nil) != valid {
						t.Fatalf("终态混合规则不符 boundary=%d count=%d duration=%d cn=%v valid=%v", boundary, count, duration, cn, valid)
					}
				}
			}
		}
	}
}

func TestMarkerResetDiscardCountRequiresActualResetBoundary(t *testing.T) {
	for _, boundary := range []uint8{1, 2, 3, 4, 5, 6} {
		m := testMarker()
		m.Boundary, m.DiscardedPackets = boundary, 17
		valid := boundary == 2 || boundary == 3 || boundary == 4 || boundary == 6
		if (m.Validate() == nil) != valid {
			t.Fatalf("边界%d重置丢弃计数无效", boundary)
		}
		if valid {
			for _, change := range []func(*Marker){func(m *Marker) { m.Reason = 8 }, func(m *Marker) { d := U64(160); m.KnownDurationTicks = &d }, func(m *Marker) { m.CNApplied = true }} {
				bad := m
				change(&bad)
				if bad.Validate() == nil {
					t.Fatal("来源清理混入其他观察字段")
				}
			}
		}
	}
}
