package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/logx"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// The context documents a retrieval span carries (adk.SpanDocuments) are large and repeat from call to call, and
// only a session that is scored needs them. With a session archive they are taken off the span as it arrives and
// kept beside it: each text once, by its hash, out of the live session. A session online evaluation decides not to
// score loses its documents; a span whose documents are gone says its input is cut.
const (
	// evidenceStoredAttr replaces a span's documents with how many were kept.
	evidenceStoredAttr = "ego.evidence.stored"
	// maxEvidenceBytesPerSession bounds what one session may keep; the documents past it are dropped, which leaves
	// the span marked cut.
	maxEvidenceBytesPerSession = 8 << 20
	// evidenceOrphanAge is how old a reference without an archived session must be before a prune removes it: a new
	// session's documents are written before the session itself.
	evidenceOrphanAge = 24 * time.Hour
)

// evidenceRow is the documents of one span, waiting to be written.
type evidenceRow struct {
	SessionID string
	SpanID    string
	Docs      []adk.ContextDoc
}

func ensureEvidenceSchema(db *sql.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS evidence_blobs (hash TEXT PRIMARY KEY, body BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS evidence_refs (
			session_id TEXT NOT NULL, span_id TEXT NOT NULL, idx INTEGER NOT NULL, doc_id TEXT NOT NULL, hash TEXT NOT NULL,
			created_ms INTEGER NOT NULL, PRIMARY KEY (session_id, span_id, idx))`,
		`CREATE INDEX IF NOT EXISTS evidence_refs_hash ON evidence_refs (hash)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("creating the evidence tables: %w", err)
		}
	}
	return nil
}

// InsertEvidence writes the documents of rows.
func (s *SQLiteStore) InsertEvidence(rows []evidenceRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer holdTimer("InsertEvidence")()
	for _, r := range rows {
		for i, d := range r.Docs {
			sum := sha256.Sum256([]byte(d.Text))
			hash := hex.EncodeToString(sum[:])
			var have int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM evidence_blobs WHERE hash = ?`, hash).Scan(&have); err != nil {
				tx.Rollback()
				return err
			}
			if have == 0 {
				var buf bytes.Buffer
				w := gzip.NewWriter(&buf)
				if _, err := w.Write([]byte(d.Text)); err != nil {
					tx.Rollback()
					return err
				}
				if err := w.Close(); err != nil {
					tx.Rollback()
					return err
				}
				if _, err := tx.Exec(`INSERT OR IGNORE INTO evidence_blobs (hash, body) VALUES (?, ?)`, hash, buf.Bytes()); err != nil {
					tx.Rollback()
					return err
				}
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO evidence_refs (session_id, span_id, idx, doc_id, hash, created_ms)
				VALUES (?, ?, ?, ?, ?, ?)`, r.SessionID, r.SpanID, i, d.ID, hash, now.UnixMilli()); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}

// SessionEvidence is the documents kept for a session, by span id, each span's in the order sent.
func (s *SQLiteStore) SessionEvidence(sessionID string) (map[string][]adk.ContextDoc, error) {
	rows, err := s.db.Query(`SELECT r.span_id, r.doc_id, b.body FROM evidence_refs r JOIN evidence_blobs b ON b.hash = r.hash
		WHERE r.session_id = ? ORDER BY r.span_id, r.idx`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]adk.ContextDoc{}
	for rows.Next() {
		var spanID, docID string
		var body []byte
		if err := rows.Scan(&spanID, &docID, &body); err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		text, err := io.ReadAll(io.LimitReader(zr, maxEvidenceBytesPerSession))
		if err != nil {
			return nil, err
		}
		out[spanID] = append(out[spanID], adk.ContextDoc{ID: docID, Text: string(text)})
	}
	return out, rows.Err()
}

// DropEvidence removes a session's references; the texts nothing refers to go with the next prune.
func (s *SQLiteStore) DropEvidence(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM evidence_refs WHERE session_id = ?`, sessionID)
	return err
}

// PruneEvidence removes the references of sessions no longer archived and then the texts nothing refers to.
func (s *SQLiteStore) PruneEvidence(now time.Time) error {
	if _, err := s.db.Exec(`DELETE FROM evidence_refs WHERE created_ms < ? AND session_id NOT IN (SELECT session_id FROM sessions)`,
		now.Add(-evidenceOrphanAge).UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.Exec(deleteOrphanEvidence)
	return err
}

// deleteOrphanEvidence removes the texts no session refers to.
const deleteOrphanEvidence = `DELETE FROM evidence_blobs WHERE NOT EXISTS (SELECT 1 FROM evidence_refs r WHERE r.hash = evidence_blobs.hash)`

// takeEvidenceLocked moves a span's documents into the pending rows, leaving on the span how many were kept. A
// session past its bound keeps no more, and the span says it is cut.
func (s *SessionStore) takeEvidenceLocked(session *Session, span *tracepkg.Span) {
	docs := adk.SpanDocuments(span)
	if len(docs) == 0 {
		return
	}
	tags := make(map[string]any, len(span.Tags)+1)
	for k, v := range span.Tags {
		if !adk.IsDocumentAttr(k) {
			tags[k] = v
		}
	}
	if size := adk.ContextBytes(docs); session.evidenceBytes+size > maxEvidenceBytesPerSession {
		tags[adk.EgoInputTruncated] = true
	} else {
		session.evidenceBytes += size
		tags[evidenceStoredAttr] = len(docs)
		s.pendingEvidence = append(s.pendingEvidence, evidenceRow{SessionID: session.ID, SpanID: span.SpanID, Docs: docs})
	}
	span.Tags = tags
}

// flushEvidence writes the buffered documents; they stay buffered when the write fails.
func (s *SessionStore) flushEvidence() error {
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	s.mu.Lock()
	rows := s.pendingEvidence
	s.pendingEvidence = nil
	s.mu.Unlock()
	if err := s.store.InsertEvidence(rows, s.now()); err != nil {
		s.mu.Lock()
		s.pendingEvidence = append(rows, s.pendingEvidence...)
		s.mu.Unlock()
		return fmt.Errorf("writing evidence documents: %w", err)
	}
	return nil
}

// DropEvidence forgets a session's documents: it is not going to be scored.
func (s *SessionStore) DropEvidence(sessionID string) {
	if s.store == nil {
		return
	}
	if err := s.flushEvidence(); err != nil {
		logx.Warnf("%v", err)
		return
	}
	if err := s.store.DropEvidence(sessionID); err != nil {
		logx.Warnf("dropping the evidence of %s: %v", sessionID, err)
	}
}

// withoutDocuments returns spans with a private copy of each span whose documents were taken off it, and the copies,
// so that putting the documents back never touches the live session. "stored" is empty for most sessions.
func withoutDocuments(spans []*tracepkg.Span) (out, stored []*tracepkg.Span) {
	out = spans
	for i, span := range spans {
		if _, ok := span.Tags[evidenceStoredAttr]; !ok {
			continue
		}
		if len(stored) == 0 {
			out = append([]*tracepkg.Span(nil), spans...)
		}
		c := *span
		c.Tags = make(map[string]any, len(span.Tags)+1)
		for k, v := range span.Tags {
			c.Tags[k] = v
		}
		out[i] = &c
		stored = append(stored, &c)
	}
	return out, stored
}

// putDocumentsBack gives each copy its documents. A span whose documents are gone, or cannot be read, says its
// input is cut, so no judge takes what is left for the whole.
func (s *SessionStore) putDocumentsBack(sessionID string, stored []*tracepkg.Span) {
	if err := s.flushEvidence(); err != nil {
		logx.Warnf("%v", err)
	}
	docs, err := s.store.SessionEvidence(sessionID)
	if err != nil {
		logx.Warnf("reading the evidence of %s: %v", sessionID, err)
	}
	for _, span := range stored {
		have := docs[span.SpanID]
		if len(have) == 0 {
			span.Tags[adk.EgoInputTruncated] = true
			continue
		}
		raw, err := json.Marshal(have)
		if err != nil {
			span.Tags[adk.EgoInputTruncated] = true
			continue
		}
		span.Tags[adk.EgoEvidenceDocuments] = string(raw)
	}
}
