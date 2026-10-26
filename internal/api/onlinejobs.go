package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/online"
)

// onlineJobsKey is the online_state key holding the jobs a person saved; absent, the file's jobs apply.
const onlineJobsKey = "jobs"

// maxUnscoredKinds bounds the kinds remembered as having no job.
const maxUnscoredKinds = 200

// countUnscored remembers that a session of kind arrived with no job to score it.
func (e *OnlineEngine) countUnscored(kind string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, seen := e.unscored[kind]; !seen && len(e.unscored) >= maxUnscoredKinds {
		return
	}
	e.unscored[kind]++
}

// loadJobs lays the saved jobs over the file's configuration. A saved document that no longer validates leaves the
// file's jobs in force and says so.
func (e *OnlineEngine) loadJobs() {
	raw, err := e.store.OnlineState(onlineJobsKey)
	if err != nil {
		logx.Warnf("online: reading saved jobs: %v", err)
		return
	}
	if raw == "" {
		return
	}
	var j online.Jobs
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		logx.Warnf("online: saved jobs are unreadable, the file's jobs stay in force: %v", err)
		return
	}
	next, err := e.base.WithJobs(j)
	if err != nil {
		logx.Warnf("online: saved jobs are invalid, the file's jobs stay in force: %v", err)
		return
	}
	e.apply(next)
	e.jobsStored.Store(true)
}

func (e *OnlineEngine) apply(c *online.Config) {
	e.cfgp.Store(c)
	e.sampler.SetConfig(c.Sampling)
}

// SetJobs validates, saves and starts using j; scoring changes from the next session on.
func (e *OnlineEngine) SetJobs(j online.Jobs) error {
	next, err := e.base.WithJobs(j)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(next.Jobs())
	if err != nil {
		return err
	}
	if err := e.store.SetOnlineState(onlineJobsKey, string(raw)); err != nil {
		return err
	}
	e.apply(next)
	e.jobsStored.Store(true)
	return nil
}

// ResetJobs drops the saved jobs and goes back to the file's.
func (e *OnlineEngine) ResetJobs() error {
	if err := e.store.SetOnlineState(onlineJobsKey, ""); err != nil {
		return err
	}
	e.apply(e.base)
	e.jobsStored.Store(false)
	return nil
}

type unscoredKindDTO struct {
	Kind     string `json:"kind"`
	Sessions int64  `json:"sessions"`
}

type onlineJobsDTO struct {
	online.Jobs
	// Source is "stored" once a person saved jobs, else "file".
	Source string `json:"source"`
	// Unscored lists the kinds that sent sessions with no job to score them, most first.
	Unscored []unscoredKindDTO `json:"unscored"`
	Budget   online.Budget     `json:"budget"`
}

func (e *OnlineEngine) jobs() onlineJobsDTO {
	c := e.conf()
	out := onlineJobsDTO{Jobs: c.Jobs(), Source: "file", Unscored: []unscoredKindDTO{}, Budget: c.Budget}
	if e.jobsStored.Load() {
		out.Source = "stored"
	}
	e.mu.Lock()
	for k, n := range e.unscored {
		out.Unscored = append(out.Unscored, unscoredKindDTO{Kind: k, Sessions: n})
	}
	e.mu.Unlock()
	sort.Slice(out.Unscored, func(i, j int) bool {
		if out.Unscored[i].Sessions != out.Unscored[j].Sessions {
			return out.Unscored[i].Sessions > out.Unscored[j].Sessions
		}
		return out.Unscored[i].Kind < out.Unscored[j].Kind
	})
	return out
}

// onlineJobsHandler implements GET, PUT and DELETE /api/online/jobs: read the jobs, replace them, go back to the
// file's. Only an unrestricted admin changes them.
func onlineJobsHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if e == nil {
			writeOnlineUnavailable(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut, http.MethodDelete:
			if !roleInfoOrOpen(r.Context()).IsAdmin {
				writeEvaluateError(w, http.StatusForbidden, "only an admin changes the scoring jobs")
				return
			}
			if r.Method == http.MethodPut {
				var j online.Jobs
				if err := decodeJSON(w, r, &j); err != nil {
					writeEvaluateError(w, http.StatusBadRequest, "invalid jobs: "+err.Error())
					return
				}
				if err := e.SetJobs(j); err != nil {
					writeEvaluateError(w, http.StatusBadRequest, err.Error())
					return
				}
			} else if err := e.ResetJobs(); err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, err.Error())
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": e.jobs(), "error": nil})
	}
}
