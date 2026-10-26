package online

import (
	"math"
	"sync"
	"time"
)

// Sampling reasons and skip reasons, as counted at /api/online/status.
const (
	ReasonError   = "error"
	ReasonOutlier = "outlier"
	ReasonChange  = "change"
	ReasonBase    = "base"

	SkipNoKind      = "no_kind"
	SkipNoProfile   = "no_profile"
	SkipDuplicate   = "duplicate"
	SkipRate        = "rate"
	SkipBudget      = "budget"
	SkipNoInvocs    = "no_invocations"
	SkipAlreadySeen = "already_seen"
	SkipQueueFull   = "queue_full"
	SkipTrivial     = "trivial"
)

// Decision is the sampler's answer for one session.
type Decision struct {
	Sample bool
	// Lane is "boost" or "base" when sampled.
	Lane string
	// Reasons lists why the session is interesting (error, outlier,
	// change), or "base" for a plain sample.
	Reasons []string
	Skip    string
}

// changeMemory is how long a (version, model) pair counts as known for a
// kind; a pair unseen for longer starts a new burst.
const changeMemory = 24 * time.Hour

// outlierMinSamples is how many sessions a kind needs before its token and
// latency baselines can flag an outlier.
const outlierMinSamples = 20

type bucket struct {
	tokens float64
	last   time.Time
}

// take refills at ratePerHour (capacity one hour's worth) and takes a token.
func (b *bucket) take(ratePerHour float64, now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = ratePerHour
	} else {
		b.tokens = math.Min(ratePerHour, b.tokens+now.Sub(b.last).Hours()*ratePerHour)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ewma is an exponentially weighted mean and variance.
type ewma struct {
	n        int
	mean, vr float64
}

const ewmaAlpha = 0.05

func (e *ewma) add(x float64) {
	e.n++
	if e.n == 1 {
		e.mean = x
		return
	}
	d := x - e.mean
	e.mean += ewmaAlpha * d
	e.vr = (1 - ewmaAlpha) * (e.vr + ewmaAlpha*d*d)
}

// above reports x more than 3 standard deviations above the mean.
func (e *ewma) above(x float64) bool {
	if e.n < outlierMinSamples || e.vr <= 0 {
		return false
	}
	return x > e.mean+3*math.Sqrt(e.vr)
}

type stream struct {
	base, boost bucket
	tokens, lat ewma
	versions    map[string]time.Time
	burst       int
}

// Sampler decides which completed sessions to score. Safe for concurrent use.
type Sampler struct {
	mu        sync.Mutex
	cfg       Sampling
	streams   map[string]*stream
	seen      map[string]time.Time // fingerprint -> last sampled
	lastPrune time.Time
}

func NewSampler(cfg Sampling) *Sampler {
	return &Sampler{cfg: cfg, streams: map[string]*stream{}, seen: map[string]time.Time{}}
}

// Decide samples or skips one session, and folds it into the kind's
// baselines either way. The caller has already matched a profile.
func (s *Sampler) Decide(f Features, now time.Time) Decision { return s.DecideAt(f, 0, now) }

// SetConfig replaces the rates and rules for the next decisions; what the sampler has learned of each kind stays.
func (s *Sampler) SetConfig(cfg Sampling) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// DecideAt is Decide with the base rate of the session's profile; 0 takes the configured per-kind rate.
func (s *Sampler) DecideAt(f Features, perHour float64, now time.Time) Decision {
	if perHour <= 0 {
		s.mu.Lock()
		perHour = s.cfg.PerKindPerHour
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	key := f.Tenant + "\x00" + f.Kind
	st := s.streams[key]
	if st == nil {
		st = &stream{versions: map[string]time.Time{}}
		s.streams[key] = st
	}

	var reasons []string
	if f.Errors > 0 {
		reasons = append(reasons, ReasonError)
	}
	lt, ll := math.Log1p(float64(f.Tokens)), math.Log1p(float64(f.LatencyMs))
	if st.tokens.above(lt) || st.lat.above(ll) {
		reasons = append(reasons, ReasonOutlier)
	}
	st.tokens.add(lt)
	st.lat.add(ll)

	vm := f.Version + "|" + f.Model
	if last, ok := st.versions[vm]; (!ok || now.Sub(last) > changeMemory) && len(st.versions) > 0 {
		st.burst = s.cfg.AfterChangeSessions
	}
	st.versions[vm] = now
	if st.burst > 0 {
		reasons = append(reasons, ReasonChange)
	}

	if len(reasons) > 0 && st.boost.take(s.cfg.BoostPerKindPerHour, now) {
		if st.burst > 0 {
			st.burst--
		}
		s.seen[f.Fingerprint] = now
		return Decision{Sample: true, Lane: "boost", Reasons: reasons}
	}
	if last, ok := s.seen[f.Fingerprint]; ok && now.Sub(last) < s.cfg.DedupWindow.D() {
		return Decision{Skip: SkipDuplicate}
	}
	if !st.base.take(perHour, now) {
		return Decision{Skip: SkipRate}
	}
	s.seen[f.Fingerprint] = now
	if len(reasons) == 0 {
		reasons = []string{ReasonBase}
	}
	return Decision{Sample: true, Lane: "base", Reasons: reasons}
}

// StartBurst boosts the next AfterChangeSessions sessions of (tenant, kind), as a new service.version does. A
// correction is a change to the agent, so the sessions that show whether it worked should be scored first. The
// tenant is the key scores are stored under (sender/label).
func (s *Sampler) StartBurst(tenant, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := tenant + "\x00" + kind
	st := s.streams[key]
	if st == nil {
		st = &stream{versions: map[string]time.Time{}}
		s.streams[key] = st
	}
	st.burst = s.cfg.AfterChangeSessions
}

func (s *Sampler) pruneLocked(now time.Time) {
	if now.Sub(s.lastPrune) < 10*time.Minute {
		return
	}
	s.lastPrune = now
	for fp, t := range s.seen {
		if now.Sub(t) > s.cfg.DedupWindow.D() {
			delete(s.seen, fp)
		}
	}
	for _, st := range s.streams {
		for vm, t := range st.versions {
			if now.Sub(t) > 2*changeMemory {
				delete(st.versions, vm)
			}
		}
	}
}
