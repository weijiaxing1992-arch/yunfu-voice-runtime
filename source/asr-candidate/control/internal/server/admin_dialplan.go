package server

import "rustswitch/control/internal/config"

// sameDialplan 比较值而非指针；GET后重新提交相同配置不应被误判为修改部署入口。
func sameDialplan(a, b *config.SIPDialplan) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
