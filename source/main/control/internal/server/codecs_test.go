package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// 探测形状与输出预算必须共同生效，旧媒体程序不可被误标为当前能力目录。
func TestAudioCapabilityContractAndBound(t *testing.T) {
	valid := []byte(`{"codecs":[{"name":"PCMU","available":true,"encode":true,"decode":true,"plc":true,"reason":""}],"pcm":{"sample_rate":16000,"channels":1,"sample_format":"s16le","frame_ms":[10,20]},"live_transcoding":false}`)
	if !validAudioCapabilities(valid) {
		t.Fatal("合法音频能力被拒绝")
	}
	for _, bad := range [][]byte{[]byte(`{"codecs":null,"pcm":null}`), bytes.ReplaceAll(valid, []byte(`"available":true`), []byte(`"available":false`)), bytes.ReplaceAll(valid, []byte(`"live_transcoding":false`), []byte(`"live_transcoding":true`)), bytes.ReplaceAll(valid, []byte(`"sample_rate":16000`), []byte(`"sample_rate":8000`))} {
		if validAudioCapabilities(bad) {
			t.Fatalf("错误能力被接受: %s", bad)
		}
	}
	var output boundedCodecOutput
	if n, err := output.Write(make([]byte, 65536)); n != 65536 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := output.Write([]byte{1}); err == nil || output.Len() != 65536 {
		t.Fatal("输出边界失效")
	}
}

// 缺少媒体程序时返回可解释失败；后续请求使用缓存，不在管理轮询中不断重启程序。
func TestCodecCatalogMissingBackend(t *testing.T) {
	var server Server
	server.Config.Media.Binary = t.TempDir() + "/missing-media"
	call := func() map[string]any {
		response := httptest.NewRecorder()
		server.getCodecs(response, httptest.NewRequest("GET", "/v1/codecs", nil))
		var value map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil {
			t.Fatal(response.Body.String())
		}
		return value
	}
	first := call()
	if first["native_audio"] != nil || first["native_probe_error"] == "" || first["live_transcoding"] != false || len(first["codec_profiles"].([]any)) != 14 {
		t.Fatal(first)
	}
	server.Config.Media.Binary = "another-nonexistent-media"
	if call()["native_probe_error"] != first["native_probe_error"] {
		t.Fatal("探测结果未按实例缓存")
	}
}
