package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"1mate/internal/model"
)

const jobColumns = `id, kind, host_id, instance_id, status, stage, exit_code, error, payload,
	log_path, created_by, created_at, started_at, finished_at`

func (s *Store) scanJob(sc interface{ Scan(...any) error }) (*model.Job, error) {
	var j model.Job
	var created string
	var started, finished sql.NullString
	if err := sc.Scan(&j.ID, &j.Kind, &j.HostID, &j.InstanceID, &j.Status, &j.Stage, &j.ExitCode,
		&j.Error, &j.Payload, &j.LogPath, &j.CreatedBy, &created, &started, &finished); err != nil {
		return nil, mapErr(err)
	}
	j.CreatedAt = parseTime(created)
	j.StartedAt = parseTimePtr(started)
	j.FinishedAt = parseTimePtr(finished)
	return &j, nil
}

func (s *Store) CreateJob(j *model.Job) error {
	ts := now()
	_, err := s.db.Exec(`INSERT INTO jobs(`+jobColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, j.HostID, j.InstanceID, j.Status, j.Stage, j.ExitCode, j.Error, j.Payload,
		j.LogPath, j.CreatedBy, ts, nil, nil)
	if err != nil {
		return err
	}
	j.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetJob(id string) (*model.Job, error) {
	return s.scanJob(s.db.QueryRow(`SELECT ` + jobColumns + ` FROM jobs WHERE id = ?`, id))
}

func (s *Store) ListJobs(filter JobFilter) ([]model.Job, error) {
	q := `SELECT ` + jobColumns + ` FROM jobs`
	var where []string
	var args []any
	if filter.HostID != "" {
		where = append(where, `host_id = ?`)
		args = append(args, filter.HostID)
	}
	if filter.InstanceID != "" {
		where = append(where, `instance_id = ?`)
		args = append(args, filter.InstanceID)
	}
	if filter.Status != "" {
		where = append(where, `status = ?`)
		args = append(args, filter.Status)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	args = append(args, filter.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Job{}
	for rows.Next() {
		j, err := s.scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

type JobFilter struct {
	HostID     string
	InstanceID string
	Status     model.JobStatus
	Limit      int
}

func (s *Store) SetJobRunning(id string) error {
	res, err := s.db.Exec(`UPDATE jobs SET status='running', started_at=? WHERE id=?`,
		now(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) SetJobStage(id, stage string) error {
	_, err := s.db.Exec(`UPDATE jobs SET stage=? WHERE id=?`, stage, id)
	return err
}

func (s *Store) FinishJob(id string, status model.JobStatus, exitCode int, errMsg string) error {
	res, err := s.db.Exec(`UPDATE jobs SET status=?, exit_code=?, error=?, finished_at=? WHERE id=?`,
		status, exitCode, errMsg, now(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) SetJobLogPath(id, path string) error {
	_, err := s.db.Exec(`UPDATE jobs SET log_path=? WHERE id=?`, path, id)
	return err
}

// MarkStaleRunningJobs fixes jobs left running by a crash so the UI never shows
// a phantom activity.
func (s *Store) MarkStaleRunningJobs() (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status='interrupted', error='platform restarted while the job was running', finished_at=? WHERE status IN ('running','queued')`, now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ActiveJobForInstance(instanceID string) (*model.Job, error) {
	return s.scanJob(s.db.QueryRow(
		`SELECT `+jobColumns+` FROM jobs WHERE instance_id=? AND status IN ('queued','running')
		 ORDER BY created_at DESC LIMIT 1`, instanceID))
}

func (s *Store) ActiveJobForHost(hostID string) (*model.Job, error) {
	return s.scanJob(s.db.QueryRow(
		`SELECT `+jobColumns+` FROM jobs WHERE host_id=? AND status IN ('queued','running')
		 ORDER BY created_at DESC LIMIT 1`, hostID))
}

func (s *Store) CreateBackup(b *model.Backup) error {
	_, err := s.db.Exec(`INSERT INTO backups(id, instance_id, filename, size_bytes, sha256, format, note, created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		b.ID, b.InstanceID, b.Filename, b.SizeBytes, b.SHA256, b.Format, b.Note,
		b.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) UpdateBackupSize(id string, size int64, sha string) error {
	_, err := s.db.Exec(`UPDATE backups SET size_bytes=?, sha256=? WHERE id=?`, size, sha, id)
	return err
}

func (s *Store) GetBackup(id string) (*model.Backup, error) {
	var b model.Backup
	var created string
	err := s.db.QueryRow(`SELECT id, instance_id, filename, size_bytes, sha256, format, note, created_at
		FROM backups WHERE id=?`, id).
		Scan(&b.ID, &b.InstanceID, &b.Filename, &b.SizeBytes, &b.SHA256, &b.Format, &b.Note, &created)
	if err != nil {
		return nil, mapErr(err)
	}
	b.CreatedAt = parseTime(created)
	return &b, nil
}

func (s *Store) ListBackups(instanceID string) ([]model.Backup, error) {
	rows, err := s.db.Query(`SELECT id, instance_id, filename, size_bytes, sha256, format, note, created_at
		FROM backups WHERE instance_id=? ORDER BY created_at DESC`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Backup{}
	for rows.Next() {
		var b model.Backup
		var created string
		if err := rows.Scan(&b.ID, &b.InstanceID, &b.Filename, &b.SizeBytes, &b.SHA256, &b.Format, &b.Note, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBackup(id string) error {
	res, err := s.db.Exec(`DELETE FROM backups WHERE id=?`, id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) CreateCredential(c *model.Credential) error {
	_, err := s.db.Exec(`INSERT INTO credentials(id, kind, ciphertext, passphrase_ciphertext, key_version, comment, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		c.ID, c.Kind, c.Ciphertext, c.PassphraseCiphertext, c.KeyVersion, c.Comment,
		c.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetCredential(id string) (*model.Credential, error) {
	var c model.Credential
	var created string
	err := s.db.QueryRow(`SELECT id, kind, ciphertext, passphrase_ciphertext, key_version, comment, created_at
		FROM credentials WHERE id=?`, id).
		Scan(&c.ID, &c.Kind, &c.Ciphertext, &c.PassphraseCiphertext, &c.KeyVersion, &c.Comment, &created)
	if err != nil {
		return nil, mapErr(err)
	}
	c.CreatedAt = parseTime(created)
	return &c, nil
}

func (s *Store) DeleteCredential(id string) error {
	if id == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM credentials WHERE id=?`, id)
	return err
}

func (s *Store) CredentialInUse(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT (SELECT COUNT(1) FROM hosts WHERE credential_id=? OR sudo_credential_id=?)
		+ (SELECT COUNT(1) FROM credentials WHERE id=?)`, id, id, id).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) EnsureHostExists(id string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("host %s: %w", id, ErrNotFound)
	}
	return nil
}
