package main

import "rustswitch/control/internal/codecprofile"

// audioProfile 的正常路径直接读取任务创建时冻结的描述符；后备分支只兼容旧单测夹具。
func (b *bench) audioProfile() codecprofile.Profile {
	if b.codecProfile.Name != "" {
		return b.codecProfile
	}
	profile, _ := codecprofile.Lookup(b.packetType)
	return profile
}
