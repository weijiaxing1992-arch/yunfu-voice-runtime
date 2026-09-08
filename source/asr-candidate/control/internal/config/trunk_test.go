package config

import "testing"

// TestTrunkConfigurationBounds 验证逻辑URI与固定网络地址分离，并拒绝注入、缺失监听和无限资源参数。
func TestTrunkConfigurationBounds(t *testing.T) {
	base, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := SIPRegistration{AOR: "sip:line@carrier.example", RegistrarURI: "sip:carrier.example:5060"}
	base.SIP.TrunkAuth = &SIPTrunkAuth{Username: "auth-line", PasswordFile: "/private/trunk-password", Realm: "carrier"}
	base.SIP.Registration = &valid
	if err = base.Validate(); err != nil {
		t.Fatal(err)
	}
	defaults := base.SIP.RegistrationOptions()
	if defaults.ExpiresSeconds != 300 || defaults.RetrySeconds != 30 || defaults.TimeoutMS != 8000 || valid.ExpiresSeconds != 0 {
		t.Fatal("默认值或独立副本不正确")
	}
	for _, change := range []func(*SIPRegistration){
		func(r *SIPRegistration) { r.AOR = "sip:line:password@carrier" },
		func(r *SIPRegistration) { r.AOR = "sip:line@carrier?secret=value" },
		func(r *SIPRegistration) { r.RegistrarURI = "sips:carrier" },
		func(r *SIPRegistration) { r.RegistrarURI = "sip:line@carrier" },
		func(r *SIPRegistration) { r.RegistrarURI = "sip:carrier\r\nInjected: value" },
		func(r *SIPRegistration) { r.ExpiresSeconds = 86401 },
		func(r *SIPRegistration) { r.ExpiresSeconds = 1 },
		func(r *SIPRegistration) { r.RetrySeconds = -1 },
		func(r *SIPRegistration) { r.TimeoutMS = 32001 },
	} {
		c := base
		r := valid
		change(&r)
		c.SIP.Registration = &r
		if c.Validate() == nil {
			t.Fatal("接受不支持或无限的注册配置")
		}
	}
	c := base
	c.SIP.Stream = &SIPStream{UpstreamTransport: "tcp"}
	if c.Validate() == nil {
		t.Fatal("可靠注册没有可回呼监听仍通过")
	}
	c = base
	c.SIP.TrunkAuth = &SIPTrunkAuth{Username: "line", PasswordFile: "relative-password"}
	if c.Validate() == nil {
		t.Fatal("接受相对密码文件路径")
	}
}
