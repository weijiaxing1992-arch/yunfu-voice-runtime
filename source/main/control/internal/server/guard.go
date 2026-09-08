package server

import (
	"errors"
	"math"
	"rustswitch/control/internal/config"
	"sync"
	"time"
)

// GuardPolicy 定义可在线修改的呼叫准入策略；所有数量均不能超过启动配置的硬上限。
// 建立中数量采用“仍占用资源的呼叫数减去已建立呼叫数”，包括结束后尚未释放的资源。
type GuardPolicy struct {
	Enabled              bool    `json:"enabled"`                // 是否启用额外阈值及接近阈值时的平滑缩速。
	MaxActiveCalls       int     `json:"max_active_calls"`       // 同时占用或预留媒体资源的呼叫上限。
	MaxEstablishingCalls int     `json:"max_establishing_calls"` // 尚未建立或仍等待释放的呼叫上限。
	CallsPerSecond       int     `json:"calls_per_second"`       // 低负载时每秒补充的新呼叫令牌数。
	BurstCalls           int     `json:"burst_calls"`            // 令牌桶容量，限制瞬间进入的新呼叫数。
	SoftLimitRatio       float64 `json:"soft_limit_ratio"`       // 达到硬阈值的这一比例后逐步降低补充速率。
	MinimumRateRatio     float64 `json:"minimum_rate_ratio"`     // 尚未到达硬阈值时保留的最小速率比例。
	RetryAfterSeconds    int     `json:"retry_after_seconds"`    // 503 返回的建议重试间隔，单位为秒。
}

// GuardDecision 是一次新呼叫检查的结果；拒绝不会排队，也不消耗令牌。
type GuardDecision struct {
	Allowed           bool   `json:"allowed"`             // 是否允许本次新呼叫预留资源。
	Reason            string `json:"reason"`              // 空字符串表示允许，其他值用于诊断拒绝来源。
	RetryAfterSeconds int    `json:"retry_after_seconds"` // 拒绝时应发送的 Retry-After 秒数。
}

// GuardSnapshot 是完全独立的值副本，可直接交给 HTTP 序列化而不暴露内部可变对象。
type GuardSnapshot struct {
	Policy               GuardPolicy   `json:"policy"`                // 当前完整策略。
	HardLimits           config.Limits `json:"hard_limits"`           // 启动时冻结的硬上限，动态更新不能抬高它。
	ActiveCalls          uint64        `json:"active_calls"`          // 当前已预留且尚未释放的呼叫数。
	EstablishedCalls     uint64        `json:"established_calls"`     // 当前已收到成功 ACK 的呼叫数。
	EstablishingCalls    uint64        `json:"establishing_calls"`    // 活跃数减已建立数，采用饱和减法防止瞬时采样下溢。
	EffectiveCPS         float64       `json:"effective_cps"`         // 当前负载下计算得到的令牌补充速率。
	Tokens               float64       `json:"tokens"`                // 当前令牌余额，可能包含尚不足一通呼叫的小数。
	Accepted             uint64        `json:"accepted"`              // 通过本保护器的新呼叫检查次数，不代表通话已接通。
	Rejected             uint64        `json:"rejected"`              // 被本保护器拒绝的新呼叫检查次数。
	RejectedActive       uint64        `json:"rejected_active"`       // 总并发阈值造成的拒绝次数。
	RejectedEstablishing uint64        `json:"rejected_establishing"` // 建立中阈值造成的拒绝次数。
	RejectedRate         uint64        `json:"rejected_rate"`         // 令牌不足造成的拒绝次数。
	LastRejectReason     string        `json:"last_reject_reason"`    // 最近一次拒绝原因，不随负载恢复而清除。
	LimitReason          string        `json:"limit_reason"`          // 当前采样下的拒绝原因，空字符串表示可以接纳至少一个呼叫。
	Throttled            bool          `json:"throttled"`             // 是否正在软阈值区间内缩速。
}

// AdmissionGuard 将策略更新、令牌与统计封装在同一个互斥锁内。
// 调用者提供原子计数快照；生产中只有呼叫主循环调用 TryAdmit 并随后增加预留数，
// HTTP 线程只调用 Snapshot/Validate/Update，因此不需要读取 calls 等主循环私有映射。
type AdmissionGuard struct {
	mu                   sync.Mutex    // 保护以下全部状态，保证一轮检查只看到一份完整策略。
	hard                 config.Limits // 启动时的不可变容量和速率上限。
	policy               GuardPolicy   // 最近成功提交的策略。
	tokens               float64       // 新呼叫令牌余额。
	lastRefill           time.Time     // 上一次令牌结算时间，保留 Go 的单调时钟信息。
	lastRate             float64       // 上一轮观察到的补充速率，更新策略前按它结算旧区间。
	lastActive           uint64        // 最近一次调用提供的活跃资源计数。
	lastEstablished      uint64        // 最近一次调用提供的已建立计数。
	accepted             uint64        // 累计允许的新呼叫数。
	rejected             uint64        // 累计拒绝的新呼叫数。
	rejectedActive       uint64        // 总并发拒绝计数。
	rejectedEstablishing uint64        // 建立中并发拒绝计数。
	rejectedRate         uint64        // 速率拒绝计数。
	lastRejectReason     string        // 最近一次拒绝原因。
}

// DefaultGuardPolicy 从硬上限生成保守的初始保护策略，兼容规模较小的开发配置。
func DefaultGuardPolicy(hard config.Limits) GuardPolicy {
	rate := min(1000, hard.CallsPerSecond)
	return GuardPolicy{
		Enabled: true, MaxActiveCalls: min(8000, hard.MaxCalls),
		MaxEstablishingCalls: min(1000, hard.MaxCalls), CallsPerSecond: rate,
		BurstCalls: min(hard.BurstCalls, rate), SoftLimitRatio: .8,
		MinimumRateRatio: .1, RetryAfterSeconds: 1,
	}
}

// NewAdmissionGuard 验证启动边界与初始策略；只有首次构造会把令牌桶填满。
func NewAdmissionGuard(hard config.Limits, policy GuardPolicy) (*AdmissionGuard, error) {
	if hard.MaxCalls < 1 || hard.CallsPerSecond < 1 || hard.BurstCalls < 1 {
		return nil, errors.New("峰值保护需要有效的启动并发、接入速率和突发上限")
	}
	g := &AdmissionGuard{hard: hard}
	if err := g.validate(policy); err != nil {
		return nil, err
	}
	g.policy = policy
	_, _, rate, burst := g.limits()
	g.tokens = float64(burst)
	g.lastRate = float64(rate)
	g.lastRefill = time.Now()
	return g, nil
}

// Validate 仅检查候选策略，不更新当前策略，也不改变令牌与统计。
// 持久化层可先调用此方法，写盘成功后再用 Update 提交相同策略。
func (g *AdmissionGuard) Validate(policy GuardPolicy) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.validate(policy)
}

// validate 在持锁时检查全部字段，禁止 NaN/无穷值绕过浮点范围比较。
func (g *AdmissionGuard) validate(policy GuardPolicy) error {
	if policy.MaxActiveCalls < 1 || policy.MaxActiveCalls > g.hard.MaxCalls {
		return errors.New("总并发上限必须大于零且不能超过启动硬上限")
	}
	if policy.MaxEstablishingCalls < 1 || policy.MaxEstablishingCalls > g.hard.MaxCalls {
		return errors.New("建立中并发上限必须大于零且不能超过启动硬上限")
	}
	if policy.CallsPerSecond < 1 || policy.CallsPerSecond > g.hard.CallsPerSecond {
		return errors.New("每秒接入速率必须大于零且不能超过启动硬上限")
	}
	if policy.BurstCalls < 1 || policy.BurstCalls > g.hard.BurstCalls {
		return errors.New("瞬时接入上限必须大于零且不能超过启动硬上限")
	}
	if math.IsNaN(policy.SoftLimitRatio) || math.IsInf(policy.SoftLimitRatio, 0) || policy.SoftLimitRatio <= 0 || policy.SoftLimitRatio >= 1 {
		return errors.New("软阈值比例必须在零和一之间")
	}
	if math.IsNaN(policy.MinimumRateRatio) || math.IsInf(policy.MinimumRateRatio, 0) || policy.MinimumRateRatio <= 0 || policy.MinimumRateRatio > 1 {
		return errors.New("最低速率比例必须大于零且不超过一")
	}
	if policy.RetryAfterSeconds < 1 || policy.RetryAfterSeconds > 3600 {
		return errors.New("建议重试间隔必须在一到三千六百秒之间")
	}
	return nil
}

// Update 原子替换完整策略；降低阈值只影响后续新呼叫，不回调或终止任何已有通话。
// 增加突发上限、反复保存或启停保护均不会自动补满令牌。
func (g *AdmissionGuard) Update(policy GuardPolicy) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.update(policy, time.Now())
}

// update 接受显式时钟以便测试更新边界；生产只通过 Update 调用。
func (g *AdmissionGuard) update(policy GuardPolicy, now time.Time) error {
	if err := g.validate(policy); err != nil {
		return err
	}
	g.refill(now, g.lastRate)
	g.policy = policy
	_, _, _, burst := g.limits()
	g.tokens = min(g.tokens, float64(burst))
	g.lastRate = g.effectiveRate(g.lastActive, g.lastEstablished)
	return nil
}

// limits 返回当前实际边界；禁用动态保护仍然保留启动配置的硬容量和令牌限制。
func (g *AdmissionGuard) limits() (active, establishing, rate, burst int) {
	if !g.policy.Enabled {
		return g.hard.MaxCalls, g.hard.MaxCalls, g.hard.CallsPerSecond, g.hard.BurstCalls
	}
	return g.policy.MaxActiveCalls, g.policy.MaxEstablishingCalls, g.policy.CallsPerSecond, g.policy.BurstCalls
}

// establishingCount 对非一致时刻读取的两个原子计数采用饱和减法，防止无符号下溢。
func establishingCount(active, established uint64) uint64 {
	if established >= active {
		return 0
	}
	return active - established
}

// effectiveRate 按两个并发阈值中更紧的一项计算线性缩速，硬阈值由准入检查另行拒绝。
func (g *AdmissionGuard) effectiveRate(active, established uint64) float64 {
	maxActive, maxEstablishing, baseRate, _ := g.limits()
	if !g.policy.Enabled {
		return float64(baseRate)
	}
	occupancy := max(float64(active)/float64(maxActive), float64(establishingCount(active, established))/float64(maxEstablishing))
	if occupancy <= g.policy.SoftLimitRatio {
		return float64(baseRate)
	}
	progress := min(1, (occupancy-g.policy.SoftLimitRatio)/(1-g.policy.SoftLimitRatio))
	ratio := 1 - progress*(1-g.policy.MinimumRateRatio)
	return float64(baseRate) * max(g.policy.MinimumRateRatio, ratio)
}

// refill 按当前限制与上一采样速率的较小值结算，避免负载刚升高时按旧高速追补大量令牌。
// 时钟回退时不补充也不后退基准；每次余额都限制在当前突发容量内。
func (g *AdmissionGuard) refill(now time.Time, rate float64) {
	_, _, _, burst := g.limits()
	if now.After(g.lastRefill) {
		elapsed := now.Sub(g.lastRefill).Seconds()
		g.tokens = min(float64(burst), g.tokens+elapsed*min(rate, g.lastRate))
		g.lastRefill = now
	}
	g.tokens = min(g.tokens, float64(burst))
}

// observe 更新一次原子计数采样和令牌余额；调用者必须持锁。
func (g *AdmissionGuard) observe(active, established uint64, now time.Time) float64 {
	rate := g.effectiveRate(active, established)
	g.refill(now, rate)
	g.lastRate = rate
	g.lastActive = active
	g.lastEstablished = established
	return rate
}

// limitReason 返回当前拒绝原因；达到硬阈值时令牌即使仍有余额也不能接入。
func (g *AdmissionGuard) limitReason(active, established uint64) string {
	maxActive, maxEstablishing, _, _ := g.limits()
	if active >= uint64(maxActive) {
		return "max_active_calls"
	}
	if establishingCount(active, established) >= uint64(maxEstablishing) {
		return "max_establishing_calls"
	}
	if g.tokens < 1 {
		return "rate_limited"
	}
	return ""
}

// TryAdmit 只处理全新的初始 INVITE；事务重传及 ACK/BYE 等已有会话消息不得调用它。
// 检查和扣除一个令牌不可分割；调用成功后由呼叫主循环马上预留实际资源。
func (g *AdmissionGuard) TryAdmit(active, established uint64, now time.Time) GuardDecision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observe(active, established, now)
	reason := g.limitReason(active, established)
	if reason == "" {
		g.tokens--
		g.accepted++
		return GuardDecision{Allowed: true}
	}
	g.rejected++
	g.lastRejectReason = reason
	switch reason {
	case "max_active_calls":
		g.rejectedActive++
	case "max_establishing_calls":
		g.rejectedEstablishing++
	case "rate_limited":
		g.rejectedRate++
	}
	return GuardDecision{Reason: reason, RetryAfterSeconds: g.policy.RetryAfterSeconds}
}

// Snapshot 返回当前策略、采样计数及诊断状态；不会消耗令牌或增加准入统计。
func (g *AdmissionGuard) Snapshot(active, established uint64, now time.Time) GuardSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	rate := g.observe(active, established, now)
	_, _, baseRate, _ := g.limits()
	return GuardSnapshot{
		Policy: g.policy, HardLimits: g.hard, ActiveCalls: active,
		EstablishedCalls: established, EstablishingCalls: establishingCount(active, established),
		EffectiveCPS: rate, Tokens: g.tokens, Accepted: g.accepted, Rejected: g.rejected,
		RejectedActive: g.rejectedActive, RejectedEstablishing: g.rejectedEstablishing,
		RejectedRate: g.rejectedRate, LastRejectReason: g.lastRejectReason,
		LimitReason: g.limitReason(active, established), Throttled: rate < float64(baseRate),
	}
}
