package providerretry

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ProviderHTTPRetry defines one provider's retry policy.
type ProviderHTTPRetry struct {
	Availability *ProviderAvailabilityRetry `yaml:"availability,omitempty"`
	RateLimit    *ProviderRatePolicy        `yaml:"rate_limit,omitempty"`
	rateGate     *ProviderRateGate
	rateOrigin   string
	MaxRetries   int                     `yaml:"max_retries"`
	MaxWaitMs    int                     `yaml:"max_wait_ms"`
	BackoffMs    []int                   `yaml:"backoff_ms"`
	Statuses     []int                   `yaml:"statuses"`
	WaitHeaders  []string                `yaml:"wait_headers"`
	Capacity     *ProviderHTTPRetry      `yaml:"capacity,omitempty"`
	Transport    *ProviderTransportRetry `yaml:"transport,omitempty"`
}

// ProviderTransportRetry handles faults without a status.
type ProviderTransportRetry struct {
	MaxRetries int   `yaml:"max_retries"`
	MaxWaitMs  int   `yaml:"max_wait_ms"`
	BackoffMs  []int `yaml:"backoff_ms"`
	// Empty Faults covers every transport fault.
	Faults []FaultKind `yaml:"faults,omitempty"`
}

// IsZero reports an unset policy.
func (p ProviderHTTPRetry) IsZero() bool {
	return p.Availability == nil && p.RateLimit == nil && p.MaxRetries == 0 && p.MaxWaitMs == 0 && len(p.BackoffMs) == 0 &&
		len(p.Statuses) == 0 && len(p.WaitHeaders) == 0 && p.Capacity == nil && p.Transport == nil
}

// DefaultTransportRetry allows one bounded retry.
func DefaultTransportRetry() ProviderTransportRetry {
	return ProviderTransportRetry{
		MaxRetries: 1,
		MaxWaitMs:  5000,
		BackoffMs:  []int{1000},
	}
}

// TransportPolicy returns the explicit or default schedule.
func (p ProviderHTTPRetry) TransportPolicy() ProviderTransportRetry {
	if p.Transport != nil {
		return *p.Transport
	}
	return DefaultTransportRetry()
}

func (t ProviderTransportRetry) Covers(kind FaultKind) bool {
	if len(t.Faults) == 0 {
		return kind == FaultUnreachable || kind == FaultSilent || kind == FaultEmptyCompletion
	}
	for _, f := range t.Faults {
		if f == kind {
			return true
		}
	}
	return false
}

// AllowsStatus includes the nested capacity schedule.
func (p ProviderHTTPRetry) AllowsStatus(code int) bool {
	for _, s := range p.Statuses {
		if s == code {
			return true
		}
	}
	if p.Capacity != nil {
		return p.Capacity.AllowsStatus(code)
	}
	return false
}

// PolicyForStatus returns the nested capacity schedule when it handles the status.
func (p ProviderHTTPRetry) PolicyForStatus(status int) ProviderHTTPRetry {
	if p.Capacity != nil && slices.Contains(p.Capacity.Statuses, status) {
		cap := *p.Capacity
		cap.Capacity = nil
		return cap
	}
	out := p
	out.Capacity = nil
	return out
}

// Clone deep-copies slice fields.
func (p ProviderHTTPRetry) Clone() ProviderHTTPRetry {
	out := p
	if p.Availability != nil {
		availability := *p.Availability
		out.Availability = &availability
	}
	if p.RateLimit != nil {
		policy := *p.RateLimit
		out.RateLimit = &policy
	}
	if p.BackoffMs != nil {
		out.BackoffMs = append([]int(nil), p.BackoffMs...)
	}
	if p.Statuses != nil {
		out.Statuses = append([]int(nil), p.Statuses...)
	}
	if p.WaitHeaders != nil {
		out.WaitHeaders = append([]string(nil), p.WaitHeaders...)
	}
	if p.Capacity != nil {
		cloned := p.Capacity.Clone()
		out.Capacity = &cloned
	}
	if p.Transport != nil {
		cloned := p.Transport.Clone()
		out.Transport = &cloned
	}
	return out
}

// Clone deep-copies slice fields.
func (t ProviderTransportRetry) Clone() ProviderTransportRetry {
	out := t
	if t.BackoffMs != nil {
		out.BackoffMs = append([]int(nil), t.BackoffMs...)
	}
	if t.Faults != nil {
		out.Faults = append([]FaultKind(nil), t.Faults...)
	}
	return out
}

// ValidateHTTPRetry checks ship/local http_retry shape.
func ValidateHTTPRetry(p ProviderHTTPRetry) error {
	if p.Availability != nil {
		if err := p.Availability.validate(); err != nil {
			return err
		}
	}
	if p.RateLimit != nil {
		if err := p.RateLimit.validate(); err != nil {
			return err
		}
		if !slices.Contains(p.Statuses, http.StatusTooManyRequests) {
			return fmt.Errorf("rate_limit requires HTTP 429 in statuses")
		}
	}
	if p.MaxRetries < 0 {
		return fmt.Errorf("http_retry.max_retries must be >= 0")
	}
	if len(p.Statuses) == 0 {
		return fmt.Errorf("http_retry.statuses must not be empty")
	}
	for _, s := range p.Statuses {
		if s < 400 || s > 599 {
			return fmt.Errorf("http_retry.statuses: %d not in 400..599", s)
		}
	}
	if p.MaxRetries > 0 {
		if p.MaxWaitMs <= 0 {
			return fmt.Errorf("http_retry.max_wait_ms must be > 0 when max_retries > 0")
		}
		if len(p.BackoffMs) < p.MaxRetries {
			return fmt.Errorf("http_retry.backoff_ms length %d < max_retries %d", len(p.BackoffMs), p.MaxRetries)
		}
		for i, ms := range p.BackoffMs {
			if ms <= 0 {
				return fmt.Errorf("http_retry.backoff_ms[%d] must be > 0", i)
			}
		}
		if len(p.WaitHeaders) == 0 {
			return fmt.Errorf("http_retry.wait_headers must not be empty when max_retries > 0")
		}
		for i, h := range p.WaitHeaders {
			if strings.TrimSpace(h) == "" {
				return fmt.Errorf("http_retry.wait_headers[%d] must not be empty", i)
			}
		}
	}
	if err := validateHTTPRetryCapacity(p); err != nil {
		return err
	}
	return validateHTTPRetryTransport(p.Transport)
}

func validateHTTPRetryCapacity(p ProviderHTTPRetry) error {
	if p.Capacity == nil {
		return nil
	}
	if p.Capacity.Capacity != nil {
		return fmt.Errorf("http_retry.capacity must not nest another capacity block")
	}
	if p.Capacity.RateLimit != nil {
		return fmt.Errorf("http_retry.capacity must not define rate_limit")
	}
	if p.Capacity.Transport != nil {
		return fmt.Errorf("http_retry.capacity must not nest a transport block; transport is status-independent")
	}
	if p.Capacity.Availability != nil {
		return fmt.Errorf("http_retry.availability belongs on the root policy")
	}
	if err := ValidateHTTPRetry(*p.Capacity); err != nil {
		return fmt.Errorf("http_retry.capacity: %w", err)
	}
	for _, s := range p.Capacity.Statuses {
		if slices.Contains(p.Statuses, s) {
			return fmt.Errorf("http_retry.statuses and http_retry.capacity.statuses both name %d", s)
		}
	}
	return nil
}

func validateHTTPRetryTransport(t *ProviderTransportRetry) error {
	if t == nil {
		return nil
	}
	if t.MaxRetries < 0 {
		return fmt.Errorf("http_retry.transport.max_retries must be >= 0")
	}
	for _, f := range t.Faults {
		if f != FaultUnreachable && f != FaultSilent && f != FaultEmptyCompletion {
			return fmt.Errorf("http_retry.transport.faults: %q is not a response retry fault (want %q, %q, or %q)",
				f, FaultUnreachable, FaultSilent, FaultEmptyCompletion)
		}
	}
	if t.MaxRetries == 0 {
		return nil
	}
	if t.MaxWaitMs <= 0 {
		return fmt.Errorf("http_retry.transport.max_wait_ms must be > 0 when max_retries > 0")
	}
	if len(t.BackoffMs) < t.MaxRetries {
		return fmt.Errorf("http_retry.transport.backoff_ms length %d < max_retries %d", len(t.BackoffMs), t.MaxRetries)
	}
	for i, ms := range t.BackoffMs {
		if ms <= 0 {
			return fmt.Errorf("http_retry.transport.backoff_ms[%d] must be > 0", i)
		}
	}
	return nil
}

// ShouldRetry checks the status retry budget.
func ShouldRetry(policy ProviderHTTPRetry, attempt int, status int) bool {
	if !policy.AllowsStatus(status) {
		return false
	}
	sub := policy.PolicyForStatus(status)
	if sub.MaxRetries <= 0 {
		return false
	}
	return attempt < sub.MaxRetries
}

// ShouldRetryFault checks the matching retry budget.
func ShouldRetryFault(policy ProviderHTTPRetry, attempt int, fault Fault) bool {
	if policy.waitsForAvailability(fault) {
		return true
	}
	switch fault.Kind {
	case FaultNone, FaultCanceled:
		return false
	case FaultModelRefused:
		return false
	case FaultUnreachable, FaultSilent, FaultEmptyCompletion:
		t := policy.TransportPolicy()
		if !t.Covers(fault.Kind) {
			return false
		}
		return attempt < t.MaxRetries
	default:
		return ShouldRetry(policy, attempt, fault.Status)
	}
}

func WaitForFault(policy ProviderHTTPRetry, attempt int, fault Fault) time.Duration {
	if policy.waitsForAvailability(fault) {
		return policy.availabilityWait(attempt, fault)
	}
	if fault.Transport() {
		t := policy.TransportPolicy()
		return ClampWait(backoffForAttempt(t.BackoffMs, attempt), time.Duration(t.MaxWaitMs)*time.Millisecond)
	}
	return waitDuration(policy.PolicyForStatus(fault.Status), attempt, fault.Header)
}

func maxAttemptsForFault(policy ProviderHTTPRetry, fault Fault) int {
	if policy.waitsForAvailability(fault) {
		return 0
	}
	if fault.Transport() {
		return policy.TransportPolicy().MaxRetries + 1
	}
	return policy.PolicyForStatus(fault.Status).MaxRetries + 1
}

// AwaitFaultRetry reports and waits for the next attempt.
func AwaitFaultRetry(ctx context.Context, policy ProviderHTTPRetry, attempt int, fault Fault) error {
	return awaitFaultRetry(ctx, policy, attempt, fault, WaitForFault(policy, attempt, fault))
}

func awaitFaultRetry(ctx context.Context, policy ProviderHTTPRetry, attempt int, fault Fault, wait time.Duration) error {
	ObserveRetry(ctx, RetryAttempt{
		// Attempt numbers start at one.
		Attempt:     attempt + 2,
		MaxAttempts: maxAttemptsForFault(policy, fault),
		Wait:        wait,
		Status:      fault.Status,
		Reason:      retryReasonForFault(fault),
		Silence:     silenceOf(fault),
	})
	return SleepHTTPRetry(ctx, wait)
}

// silenceOf returns only observed post-delivery silence.
func silenceOf(f Fault) time.Duration {
	if f.Kind != FaultSilent {
		return 0
	}
	return f.Elapsed
}

// waitDuration returns how long to sleep before the next attempt.
func waitDuration(policy ProviderHTTPRetry, attempt int, hdr http.Header) time.Duration {
	maxWait := time.Duration(policy.MaxWaitMs) * time.Millisecond
	if server, ok := headerWait(policy.WaitHeaders, hdr); ok {
		return server
	}
	backoff := backoffForAttempt(policy.BackoffMs, attempt)
	if backoff > 0 {
		// Retry jitter is not a security boundary.
		jitter := time.Duration(rand.Intn(251)) * time.Millisecond // #nosec G404
		return ClampWait(backoff+jitter, maxWait)
	}
	return 0
}

// Every declared server deadline is a lower bound, independent of the fallback cap.
func headerWait(names []string, hdr http.Header) (time.Duration, bool) {
	var longest time.Duration
	found := false
	for _, name := range names {
		if delay, ok := parseWaitHeader(strings.TrimSpace(name), hdr); ok {
			longest = max(longest, delay)
			found = true
		}
	}
	return longest, found
}

func numericWait(value string, unit time.Duration) (time.Duration, bool) {
	amount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount >= float64(math.MaxInt64)/float64(unit) {
		return 0, false
	}
	return time.Duration(amount * float64(unit)), true
}

// SleepHTTPRetry sleeps d or returns ctx.Err().
func SleepHTTPRetry(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func backoffForAttempt(backoffMs []int, attempt int) time.Duration {
	if len(backoffMs) == 0 {
		return 0
	}
	idx := attempt
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffMs) {
		idx = len(backoffMs) - 1
	}
	return time.Duration(backoffMs[idx]) * time.Millisecond
}

func ClampWait(d, maxWait time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if maxWait > 0 && d > maxWait {
		return maxWait
	}
	return d
}

func parseWaitHeader(name string, hdr http.Header) (time.Duration, bool) {
	if hdr == nil {
		return 0, false
	}
	val := headerGetCI(hdr, name)
	if val == "" {
		return 0, false
	}
	switch strings.ToLower(name) {
	case "retry-after":
		return parseRetryAfter(val)
	case "retry-after-ms", "x-ms-retry-after-ms":
		return numericWait(val, time.Millisecond)
	case "x-ratelimit-reset":
		return numericWait(val, time.Second)
	default:
		return 0, false
	}
}

func headerGetCI(hdr http.Header, name string) string {
	if v := hdr.Get(name); v != "" {
		return v
	}
	// Preserve non-canonical header names.
	for k, vals := range hdr {
		if strings.EqualFold(k, name) && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func parseRetryAfter(val string) (time.Duration, bool) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, false
	}
	if sec, err := strconv.Atoi(val); err == nil {
		if sec < 0 {
			return 0, false
		}
		if sec > math.MaxInt64/int(time.Second) {
			return 0, false
		}
		return time.Duration(sec) * time.Second, true
	}
	t, err := http.ParseTime(val)
	if err != nil {
		return 0, false
	}
	d := time.Until(t)
	if d < 0 {
		return 0, true
	}
	return d, true
}

// BindRateGate attaches the registry's shared admission gate to a retry policy.
func (p ProviderHTTPRetry) BindRateGate(gate *ProviderRateGate, origin string) ProviderHTTPRetry {
	p.rateGate = gate
	p.rateOrigin = origin
	return p
}
