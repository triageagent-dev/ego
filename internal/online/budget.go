package online

import (
	"sync"
	"time"
)

// Cost is the judge spend of one scored session.
type Cost struct {
	JudgeTokens int64 `json:"judgeTokens"`
	JevCalls    int64 `json:"jevCalls"`
}

// BudgetMeter meters judge spend against the daily caps. Before a session is
// scored, Reserve books an estimate (an EWMA of the profile's actual cost,
// or a fixed first guess); Settle replaces it with the actual cost. Spend
// resets at 00:00 UTC. Safe for concurrent use.
type BudgetMeter struct {
	mu       sync.Mutex
	caps     Budget
	day      string
	spent    Cost
	reserved Cost
	estimate map[string]Cost
}

// Initial estimates before a profile has been scored once.
const (
	firstJudgeTokens = 4000
	firstJevCalls    = 3
)

func NewBudgetMeter(caps Budget) *BudgetMeter {
	return &BudgetMeter{caps: caps, estimate: map[string]Cost{}}
}

func (b *BudgetMeter) rollLocked(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if day != b.day {
		b.day = day
		b.spent = Cost{}
	}
}

// Estimate is the expected cost of scoring one session with p.
func (b *BudgetMeter) estimateLocked(p Profile) Cost {
	if e, ok := b.estimate[p.Key()]; ok {
		return e
	}
	var c Cost
	if p.UsesJudge() {
		c.JudgeTokens = firstJudgeTokens
	}
	if p.UsesJev() {
		c.JevCalls = firstJevCalls
	}
	return c
}

// Reserve books the estimate for p if it fits under both caps today.
func (b *BudgetMeter) Reserve(p Profile, now time.Time) (Cost, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(now)
	est := b.estimateLocked(p)
	if est.JudgeTokens > 0 && b.spent.JudgeTokens+b.reserved.JudgeTokens+est.JudgeTokens > b.caps.JudgeTokensPerDay {
		return Cost{}, false
	}
	if est.JevCalls > 0 && b.spent.JevCalls+b.reserved.JevCalls+est.JevCalls > b.caps.JevCallsPerDay {
		return Cost{}, false
	}
	b.reserved.JudgeTokens += est.JudgeTokens
	b.reserved.JevCalls += est.JevCalls
	return est, true
}

// Settle releases a reservation and books the actual cost. A nil actual
// (the session was not scored after all) only releases.
func (b *BudgetMeter) Settle(p Profile, reserved Cost, actual *Cost, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved.JudgeTokens -= reserved.JudgeTokens
	b.reserved.JevCalls -= reserved.JevCalls
	b.rollLocked(now)
	if actual == nil {
		return
	}
	b.spent.JudgeTokens += actual.JudgeTokens
	b.spent.JevCalls += actual.JevCalls
	prev, ok := b.estimate[p.Key()]
	if !ok {
		b.estimate[p.Key()] = *actual
		return
	}
	const a = 0.2
	b.estimate[p.Key()] = Cost{
		JudgeTokens: int64(float64(prev.JudgeTokens)*(1-a) + float64(actual.JudgeTokens)*a),
		JevCalls:    int64(float64(prev.JevCalls)*(1-a) + float64(actual.JevCalls)*a + 0.5),
	}
}

// Restore sets today's spend after a restart (from online_scores).
func (b *BudgetMeter) Restore(spent Cost, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(now)
	b.spent = spent
}

// BudgetStatus is the meter's state for /api/online/status.
type BudgetStatus struct {
	Day      string `json:"day"`
	Caps     Budget `json:"caps"`
	Spent    Cost   `json:"spent"`
	Reserved Cost   `json:"reserved"`
}

func (b *BudgetMeter) Status(now time.Time) BudgetStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(now)
	return BudgetStatus{Day: b.day, Caps: b.caps, Spent: b.spent, Reserved: b.reserved}
}
