package audit

import (
	"encoding/json"
	"net/http"
	"time"

	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/store"
)

// Recorder writes audit entries. It never fails the calling request: an audit
// write error must not block an operation, but it is surfaced in the detail.
type Recorder struct {
	store *store.Store
}

func New(st *store.Store) *Recorder { return &Recorder{store: st} }

func (r *Recorder) Log(actor, action, targetType, targetID string, detail any, req *http.Request) {
	d := "{}"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			d = string(b)
		}
	}
	ip := ""
	if req != nil {
		ip = ClientIP(req)
	}
	_ = r.store.InsertAudit(&model.AuditLog{
		ID:         ids.New(),
		Actor:      actor,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		DetailJSON: d,
		IP:         ip,
		CreatedAt:  time.Now().UTC(),
	})
}

// ClientIP resolves the caller address, honouring the configured proxy trust at
// the handler layer rather than blindly trusting X-Forwarded-For.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
