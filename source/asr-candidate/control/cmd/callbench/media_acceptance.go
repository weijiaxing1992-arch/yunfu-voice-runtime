// 本文件把实际媒体负载与缺包约束落实到每通呼叫的每个方向，避免总量掩盖局部空载。
package main

// flowLoadSummary 在全部收发工作者结束后读取稳定计数；不参与热路径，也不改变原始计数。
type flowLoadSummary struct {
	underloadedDirections uint64 // 发送或接收不足该方向名义包量98%的方向数。
	missingPackets        uint64 // 各方向正向缺包之和，不允许另一方向的多收包抵消。
	unexpectedReceived    uint64 // 超过对应方向本地成功发送数的唯一接收数，同样属于失败。
}

// sufficientDirectionLoad 使用整数比例判断，明确拒绝零发送/零接收，不受浮点舍入影响。
func sufficientDirectionLoad(sent, received, nominal uint64) bool {
	return nominal > 0 && sent > 0 && received > 0 && sent*100 >= nominal*98 && received*100 >= nominal*98
}

// assessFlowLoad 按 side 到 side^1 配对；只有成功建立的呼叫进入媒体计数，建立数量另行严格验收。
func assessFlowLoad(flows []*flow, nominalPerDirection uint64) flowLoadSummary {
	var result flowLoadSummary
	for _, f := range flows {
		if !f.accepted.Load() {
			continue
		}
		for side := 0; side < 2; side++ {
			sent, received := f.sent[side], f.received[side^1]
			if !sufficientDirectionLoad(sent, received, nominalPerDirection) {
				result.underloadedDirections++
			}
			if sent > received {
				result.missingPackets += sent - received
			} else {
				result.unexpectedReceived += received - sent
			}
		}
	}
	return result
}
