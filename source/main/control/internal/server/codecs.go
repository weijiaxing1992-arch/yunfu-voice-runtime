package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"rustswitch/control/internal/codecprofile"
)

// codecProbeCache 每个控制实例只探测一次可信媒体程序，管理轮询不持续派生子进程。
type codecProbeCache struct {
	once  sync.Once
	audio json.RawMessage // 自检能力原文来自当前配置的媒体可执行文件，不据库名推测已加载。
	err   string
}

// boundedCodecOutput 对异常或版本不符的子程序限制输出，避免只读探测占用无界内存。
type boundedCodecOutput struct{ bytes.Buffer }

func (b *boundedCodecOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("音频能力输出超过64KiB上限")
	}
	return b.Buffer.Write(p)
}

// getCodecs 返回真实传输实现与本地音频后端能力；原生库缺失不伪装为可转码。
func (s *Server) getCodecs(w http.ResponseWriter, r *http.Request) {
	s.codecInfo.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, s.Config.Media.Binary, "--audio-capabilities")
		command.WaitDelay = 500 * time.Millisecond
		var output, diagnostic boundedCodecOutput
		command.Stdout, command.Stderr = &output, &diagnostic
		if err := command.Run(); err != nil {
			s.codecInfo.err = "原生音频能力探测未成功：" + err.Error()
			return
		}
		if !validAudioCapabilities(output.Bytes()) {
			s.codecInfo.err = "媒体程序没有返回当前版本的音频能力合同"
			return
		}
		s.codecInfo.audio = append(json.RawMessage(nil), output.Bytes()...)
	})
	runtime := s.runtimeCodecs()
	mode := "same_codec_rtp_passthrough"
	if runtime.Enabled {
		mode = "g711_audio_graph"
	}
	writeJSON(w, map[string]any{
		"version": "1.2.0", "media_mode": mode, "live_transcoding": runtime.Ready,
		"runtime_media": runtime, "live_codec_profiles": liveCodecProfiles(runtime.Processing),
		"codec_profiles": codecprofile.List(), "native_audio": s.codecInfo.audio, "native_probe_error": s.codecInfo.err,
		"scope": "实时模式由有效配置和当前 worker 握手共同确定；relay 为同编码 RTP 透传，g711 为 PCMA/PCMU 双腿音频处理，本地 A 腿额外由 runtime_media.local_ready 标示。live_transcoding 仍仅表示桥接就绪。codec_profiles 保留全部测试预设，live_codec_profiles 为当前模式允许的格式。离线 SDK 探测独立列示；ASR/TTS 与实时录音尚待接入。",
		"roadmap": []map[string]string{
			{"name": "G.729 / G.729A", "priority": "P1", "status": "RTP直通；原生编码/解码后端待接入"},
			{"name": "AMR-NB / AMR-WB", "priority": "P1", "status": "规划；RTP解包与编码后端尚未实现，SDP明确拒绝"},
			{"name": "EVS", "priority": "P2", "status": "面向未来IMS接入；当前未实现"},
			{"name": "iLBC / G.723.1 / GSM", "priority": "P2/P3", "status": "按实际线路需求接入；当前未实现"},
		},
	})
}

// validAudioCapabilities 拒绝旧版本或形状含混的输出，避免页面将错误对象误读为后端列表。
// 动态字段允许扩展，但基础 PCM、布尔能力与显式非实时转码标识必须存在且一致。
func validAudioCapabilities(raw []byte) bool {
	var value struct {
		Codecs []struct {
			Name      string  `json:"name"`
			Available *bool   `json:"available"`
			Encode    *bool   `json:"encode"`
			Decode    *bool   `json:"decode"`
			PLC       *bool   `json:"plc"`
			Reason    *string `json:"reason"`
		} `json:"codecs"`
		PCM struct {
			SampleRate   int    `json:"sample_rate"`
			Channels     int    `json:"channels"`
			SampleFormat string `json:"sample_format"`
			FrameMS      []int  `json:"frame_ms"`
		} `json:"pcm"`
		LiveTranscoding *bool `json:"live_transcoding"`
	}
	if json.Unmarshal(raw, &value) != nil || len(value.Codecs) == 0 || len(value.Codecs) > 64 || value.LiveTranscoding == nil || *value.LiveTranscoding || value.PCM.SampleRate != 16000 || value.PCM.Channels != 1 || value.PCM.SampleFormat != "s16le" || len(value.PCM.FrameMS) != 2 || value.PCM.FrameMS[0] != 10 || value.PCM.FrameMS[1] != 20 {
		return false
	}
	names := make(map[string]bool, len(value.Codecs))
	for _, codec := range value.Codecs {
		if codec.Name == "" || names[codec.Name] || codec.Available == nil || codec.Encode == nil || codec.Decode == nil || codec.PLC == nil || codec.Reason == nil {
			return false
		}
		if !*codec.Available && (*codec.Encode || *codec.Decode || *codec.PLC) {
			return false
		}
		names[codec.Name] = true
	}
	return true
}
