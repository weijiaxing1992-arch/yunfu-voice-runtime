// Package asr在Go适配层连接独立识别代理；媒体worker不等待供应商或业务消费。
package asr

import (
	"context"
	"errors"
	"time"

	"rustswitch/control/internal/media"
)

var (
	ErrInvalid            = errors.New("invalid ASR stream options or source")
	ErrUnavailable        = errors.New("ASR provider unavailable")
	ErrProtocol           = errors.New("ASR provider protocol violation")
	ErrCancelled          = errors.New("ASR stream cancelled or call lifetime ended")
	ErrBusy               = errors.New("ASR operation already in progress")
	ErrInputFailed        = errors.New("ASR source ended without an explicit finish")
	ErrSubmissionUnknown  = errors.New("ASR partial submission outcome unknown; connection closed")
	ErrResultOverflow     = errors.New("ASR result queue exceeded its fixed capacity")
	ErrProvenanceOverflow = errors.New("ASR submitted audio provenance exceeded its fixed capacity")
	ErrProviderTimeout    = errors.New("ASR provider deadline exceeded")
)

// Source只包含已授权收音和真实清理证据，不暴露Call映射或任何下行PCM操作。
// ConfirmedRetired只能由已观察的底层退休/全池关闭证明，服务ctx取消本身不能证明释放。
type Source interface {
	Read(context.Context) (media.RXFrame, error)
	Unsubscribe(context.Context) (media.Reply, error)
	Status(context.Context) (media.Reply, error)
	Snapshot() media.RXSnapshot
	LifetimeDone() <-chan struct{}
	ConfirmedRetired() bool
}

// Options来自启动冻结的部署配置；不接受每条音频携带的任意目标或静默格式协商。
type Options struct {
	SocketPath     string
	UUID           string
	SubscriptionID uint64
	SampleRate     uint32
}

// Acquire在供应商握手完成后获取真实ACK/local/同代RX；未知受理可返回非nil清理身份与error。
type Acquire func(context.Context) (Source, error)

// Event是适配器向业务交付的有界结果；文本只出现在该显式消费接口，不写指标标签。
type Event struct {
	Type               string `json:"type"`
	Text               string `json:"text,omitempty"`
	Coverage           string `json:"coverage,omitempty"`
	ResultSequence     uint64 `json:"result_sequence"`
	UtteranceID        uint64 `json:"utterance_id"`
	Revision           uint64 `json:"revision"`
	SourceGeneration   uint64 `json:"source_generation"`
	SourceSegment      uint64 `json:"source_segment"`
	FirstEventSequence uint64 `json:"first_event_sequence"`
	LastEventSequence  uint64 `json:"last_event_sequence"`
	StartMediaNS       uint64 `json:"start_media_ns"`
	EndMediaNS         uint64 `json:"end_media_ns"`
}

// Snapshot区分实际提交、代理确认、业务交付与资源回收；任何单项都不是识别成功通话数。
type Snapshot struct {
	State                       string `json:"state"`
	SampleRate                  uint32 `json:"sample_rate"`
	InputFinished               bool   `json:"input_finished"`
	ProviderDone                bool   `json:"provider_done"`
	RXStopped                   bool   `json:"rx_stopped"`
	TransportClosed             bool   `json:"transport_closed"`
	CleanupPending              bool   `json:"cleanup_pending"`
	ResourcesClosed             bool   `json:"resources_closed"`
	AudioFrames                 uint64 `json:"adapter_written_audio_frames"`
	Samples                     uint64 `json:"adapter_written_samples"`
	Markers                     uint64 `json:"adapter_written_markers"`
	ProviderAcknowledgedSamples uint64 `json:"provider_acknowledged_samples"`
	ExpiredFrames               uint64 `json:"expired_frames"`
	UnknownWrites               uint64 `json:"unknown_writes"`
	ResultsReceived             uint64 `json:"results_received"`
	ResultsDelivered            uint64 `json:"results_delivered"`
	StaleResults                uint64 `json:"stale_results"`
	PartialCoalesced            uint64 `json:"partial_coalesced"`
	PartialInvalidated          uint64 `json:"partial_invalidated"`
	QueuedResults               int    `json:"queued_results"`
	ProvenanceRanges            int    `json:"provenance_ranges"`
	LastEventSequence           uint64 `json:"last_event_sequence"`
	Error                       string `json:"error,omitempty"`
}

const (
	resultLimit             = 8
	provenanceLimit         = 64
	frameDurationNS  uint64 = 20_000_000
	handshakeTimeout        = 2 * time.Second
	acquireTimeout          = time.Second
	finishTimeout           = 2 * time.Second
	frameTimeout            = time.Second
	pingInterval            = 10 * time.Second
	pongTimeout             = 2 * time.Second
)
