package aistudio

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const quotaResetTimezone = "America/Los_Angeles"

// planLimitBackoff defines escalated cooldown durations by consecutive count for tier limits without recovery time.
var planLimitBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

// quotaLimitPattern matches listed quota limits in quota errors.
var quotaLimitPattern = regexp.MustCompile(`limit: (\d+)`)

// CooldownState represents the state where an account or model is temporarily unschedulable
type CooldownState struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason,omitempty"`
	Level  int       `json:"level,omitempty"`
}

// Active checks whether the cooldown state is currently active
func (c CooldownState) Active(now time.Time) bool {
	return !c.Until.IsZero() && now.Before(c.Until)
}

// QuotaCooldown represents the scheduling cooldown corresponding to upstream quota limits
type QuotaCooldown struct {
	Until    time.Time
	Global   bool
	Kind     string
	Reason   string
	Escalate bool
}

// QuotaCooldownForError parses upstream minute or daily quota limits
func QuotaCooldownForError(err error, now time.Time) (QuotaCooldown, bool) {
	var rpcError *RPCError
	if !errors.As(err, &rpcError) || rpcError.StatusCode != http.StatusTooManyRequests {
		return QuotaCooldown{}, false
	}
	metadata := strings.ToLower(strings.Join([]string{
		rpcError.Metadata["quota_metric"],
		rpcError.Metadata["quota_limit"],
		rpcError.Metadata["quota_unit"],
	}, " "))
	message := strings.ToLower(rpcError.Message)
	global := (strings.Contains(metadata, "_global") || strings.Contains(metadata, "perprojectperuser")) &&
		!strings.Contains(metadata, "per_model") && !strings.Contains(metadata, "permodel") && !strings.Contains(metadata, "{model}")
	until, kind := now.Add(time.Minute), "短期限额"
	for _, evidence := range []string{rpcError.Metadata["quota_unit"], rpcError.Metadata["quota_limit"], rpcError.Metadata["quota_metric"], message} {
		evidence = strings.ToLower(evidence)
		if dailyQuotaEvidence(evidence) {
			until, kind = nextQuotaDay(now), "每日限额"
			break
		}
		if minuteQuotaEvidence(evidence) {
			until, kind = minuteQuotaReset(rpcError.Metadata["window_start_time"], now), "分钟限额"
			break
		}
	}
	if zeroQuotaLimit(rpcError.Message) {
		kind = "无配额"
	}
	escalate := false
	if rpcError.RetryDelay > 0 {
		until = now.Add(rpcError.RetryDelay)
	} else if kind == "短期限额" && strings.Contains(message, "reached your usage limit") {
		kind, escalate = "套餐限额", true
	}
	return QuotaCooldown{Until: until, Global: global, Kind: kind, Reason: kind + ": " + err.Error(), Escalate: escalate}, true
}

// BuildRejectionCooldown returns escalated Build cooldown for requests rejected with invalid arguments by Build proxy and completed by Playground on the same account.
func BuildRejectionCooldown(err error) (QuotaCooldown, bool) {
	var rpcError *RPCError
	if !errors.As(err, &rpcError) || rpcError.StatusCode != http.StatusBadRequest || rpcError.Code != 3 {
		return QuotaCooldown{}, false
	}
	return QuotaCooldown{Kind: "Build 拒绝", Reason: "Build 拒绝: " + err.Error(), Escalate: true}, true
}

// zeroQuotaLimit checks whether all quota limit items listed in a quota error are 0.
func zeroQuotaLimit(message string) bool {
	matches := quotaLimitPattern.FindAllStringSubmatch(message, -1)
	for _, match := range matches {
		if match[1] != "0" {
			return false
		}
	}
	return len(matches) > 0
}

// planLimitCooldown calculates the next cooldown based on the previous plan limit cooldown in the same scope; retains previous when it hasn't expired.
func planLimitCooldown(previous CooldownState, exists bool, now time.Time, reason string) CooldownState {
	level := 1
	if exists && previous.Level > 0 {
		if previous.Active(now) {
			return previous
		}
		level = min(previous.Level+1, len(planLimitBackoff))
	}
	return CooldownState{Until: now.Add(planLimitBackoff[level-1]).UTC(), Reason: reason, Level: level}
}

func minuteQuotaEvidence(value string) bool {
	return strings.Contains(value, "/min/") || strings.Contains(value, "perminute") ||
		strings.Contains(value, "per_min") || strings.Contains(value, "per minute")
}

func dailyQuotaEvidence(value string) bool {
	return strings.Contains(value, "/day/") || strings.Contains(value, "perday") ||
		strings.Contains(value, "per_day") || strings.Contains(value, "per day") ||
		strings.Contains(value, "daily limit") || strings.Contains(value, "try again tomorrow")
}

func minuteQuotaReset(windowStart string, now time.Time) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(windowStart), 10, 64)
	if err == nil {
		until := time.Unix(seconds, 0).Add(time.Minute)
		if until.After(now) && !until.After(now.Add(time.Minute)) {
			return until
		}
	}
	return now.Add(time.Minute)
}

func nextQuotaDay(now time.Time) time.Time {
	location, err := time.LoadLocation(quotaResetTimezone)
	if err != nil {
		panic(err)
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
}
