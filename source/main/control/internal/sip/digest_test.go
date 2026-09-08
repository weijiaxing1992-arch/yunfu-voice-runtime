package sip

import (
	"strings"
	"testing"
)

// TestDigestPublishedVectors 使用 RFC 2617/7616 已公布值，避免编码器与测试器同错而自证。
func TestDigestPublishedVectors(t *testing.T) {
	for _, test := range []struct{ name, algorithm, realm, nonce, password, cnonce, want string }{
		{"rfc2617_md5", "MD5", "testrealm@host.com", "dcd98b7102dd2f0e8b11d0f600bfb0c093", "Circle Of Life", "0a4f113b", "6629fae49393a05397450978507c4ef1"},
		{"rfc7616_md5", "MD5", "http-auth@example.org", "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", "Circle of Life", "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ", "8ca523f5e9506fed4657c9700eebdbec"},
		{"rfc7616_sha256", "SHA-256", "http-auth@example.org", "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", "Circle of Life", "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ", "753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := ParseDigestChallenge(`Digest realm="` + test.realm + `", nonce="` + test.nonce + `", qop="auth,auth-int", algorithm=` + test.algorithm)
			if err != nil {
				t.Fatal(err)
			}
			header, err := c.Authorization("Mufasa", test.password, "GET", "/dir/index.html", test.cnonce, 1)
			if err != nil {
				t.Fatal(err)
			}
			values, err := DigestParameters(header)
			if err != nil || values["response"] != test.want || values["nc"] != "00000001" {
				t.Fatal("公布摘要向量不符")
			}
		})
	}
}

// TestDigestRejectsAmbiguousChallenges 将畸形和未支持挑战维持为失败，不放宽为默认 MD5。
func TestDigestRejectsAmbiguousChallenges(t *testing.T) {
	base := `Digest realm="carrier", nonce="nonce"`
	for _, test := range []string{"Basic abc", base + `, realm="other"`, base + `, nonce="twice"`, base + ",\r\nInjected: value", base + ",", base + `, x="unterminated`, base + `, qop="auth-int"`, base + `, qop=""`, base + `, algorithm=AKAv1-MD5`, base + `, userhash=true`, base + `, charset=ISO-8859-1`, base + `, stale=maybe`, strings.Repeat("x", 4097), `Digest realm="carrier"`} {
		if _, err := ParseDigestChallenge(test); err == nil {
			t.Fatal("接受了歧义或未支持挑战")
		}
	}
	c, err := ParseDigestChallenge(`Digest realm="a,b", nonce="n\"once", opaque="back\\slash", qop="auth", algorithm=SHA-512-256-sess`)
	if err != nil || c.Realm != "a,b" || c.Nonce != "n\"once" || c.Opaque != "back\\slash" {
		t.Fatal("带转义挑战未正确解析")
	}
	for _, algorithm := range []string{"MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS", "SHA-512-256", "SHA-512-256-SESS"} {
		t.Run(algorithm, func(t *testing.T) {
			c.Algorithm = algorithm
			header, err := c.Authorization("user", "TEST_ONLY", "INVITE", "sip:100@carrier", "0123456789abcdef", 2)
			if err != nil {
				t.Fatal(err)
			}
			p, err := DigestParameters(header)
			if err != nil || p["realm"] != c.Realm || p["nonce"] != c.Nonce || p["opaque"] != c.Opaque || p["nc"] != "00000002" {
				t.Fatal("认证头转义/计数不正确")
			}
		})
	}
}

// TestDigestLegacyAndSessionVectors 使用独立Python/OpenSSL预计算值覆盖全部算法、sess与旧式无qop。
func TestDigestLegacyAndSessionVectors(t *testing.T) {
	for _, test := range []struct{ algorithm, legacy, auth string }{
		{"MD5", "1ecfdd79dc4776efe486beb179cb416c", "c836dda527ad10deca9f9f8e1b133ab8"},
		{"MD5-SESS", "462b3324f33fe36b6c524700dff41502", "6ad0bb5d8ea1c2812538fd631ad0b7e5"},
		{"SHA-256", "1897d983fe1935af57337e3b2545f84ffee1d25d3fb2e406359e807b3f716ccb", "9a0c8eeb9fae71f712df9c0aea1cdb8144773054f27af8d5092d83b5c3dcf06d"},
		{"SHA-256-SESS", "5a8882f9de8c125ff26f677e27306135ec60ffc8405a2b161503dfcf45b15fb1", "cb281df83592891be3c98e5670c01b1a47035099bd10cf205d447dc62382da55"},
		{"SHA-512-256", "76ea3e4d990530877938dc4b3f84f5aeb3f4139c387f90282753bc372b700aa4", "1058d726bda43a47161b9913d8dd8187b450cfb7ad9a04bd1a01f8164598598b"},
		{"SHA-512-256-SESS", "c44e0e0896e768f3339e139a63d89aa1bbfc2dfc34d63d219a74609818858c36", "59c80556f3e543e2a59f38c582050356552a1db3346ae9b3a359128f56a809db"},
	} {
		for _, qop := range []string{"", "auth"} {
			t.Run(test.algorithm+"/"+qop, func(t *testing.T) {
				c := DigestChallenge{Realm: "carrier", Nonce: "nonce", Algorithm: test.algorithm, QOP: qop}
				header, err := c.Authorization("line", "TEST_ONLY_PASSWORD", "REGISTER", "sip:carrier", "0123456789abcdef", 2)
				if err != nil {
					t.Fatal(err)
				}
				p, err := DigestParameters(header)
				want := test.auth
				if qop == "" {
					want = test.legacy
				}
				if err != nil || p["response"] != want || p["qop"] != qop || qop == "" && p["nc"] != "" {
					t.Fatal("独立计算摘要或旧式参数不符")
				}
			})
		}
	}
}
