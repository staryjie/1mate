package store

import (
	"encoding/json"
	"time"

	"1mate/internal/model"
)

const instanceColumns = `id, name, slug, host_id, project_name, base_dir, bind_address, tm_port,
	grafana_port, mqtt_port, timezone, region, image_tag, postgres_tag, grafana_admin_user,
	disable_mqtt, extra_env, source, tm_encryption_key, db_user, db_pass, grafana_pass, compose_major,
	compose_sha256, status, status_message, last_job_id, created_at, updated_at`

func (s *Store) scanInstance(sc interface{ Scan(...any) error }) (*model.Instance, error) {
	var in model.Instance
	var disableMQTT int
	var extraEnv, source, created, updated string
	if err := sc.Scan(&in.ID, &in.Name, &in.Slug, &in.HostID, &in.ProjectName, &in.BaseDir,
		&in.BindAddress, &in.TMPort, &in.GrafanaPort, &in.MQTTPort, &in.Timezone, &in.Region,
		&in.ImageTag, &in.PostgresTag, &in.GrafanaAdminUser, &disableMQTT, &extraEnv, &source,
		&in.TMEncryptionKey, &in.DBUser, &in.DBPass, &in.GrafanaPass, &in.ComposeMajor,
		&in.ComposeSHA256, &in.Status, &in.StatusMessage, &in.LastJobID, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	in.DisableMQTT = disableMQTT != 0
	in.Source = source
	in.ExtraEnv = map[string]string{}
	if extraEnv != "" {
		_ = json.Unmarshal([]byte(extraEnv), &in.ExtraEnv)
	}
	in.CreatedAt = parseTime(created)
	in.UpdatedAt = parseTime(updated)
	return &in, nil
}

func (s *Store) CreateInstance(in *model.Instance) error {
	ts := now()
	extra, err := json.Marshal(in.ExtraEnv)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO instances(`+instanceColumns+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ID, in.Name, in.Slug, in.HostID, in.ProjectName, in.BaseDir, in.BindAddress,
		in.TMPort, in.GrafanaPort, in.MQTTPort, in.Timezone, in.Region, in.ImageTag,
		in.PostgresTag, in.GrafanaAdminUser, boolInt(in.DisableMQTT), string(extra),
		in.Source, in.TMEncryptionKey, in.DBUser, in.DBPass, in.GrafanaPass, in.ComposeMajor,
		in.ComposeSHA256, in.Status, in.StatusMessage, in.LastJobID, ts, ts)
	if err != nil {
		return err
	}
	in.CreatedAt, in.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) GetInstance(id string) (*model.Instance, error) {
	return s.scanInstance(s.db.QueryRow(`SELECT `+instanceColumns+` FROM instances WHERE id = ?`, id))
}

func (s *Store) GetInstanceBySlug(hostID, slug string) (*model.Instance, error) {
	return s.scanInstance(s.db.QueryRow(
		`SELECT `+instanceColumns+` FROM instances WHERE host_id = ? AND slug = ?`, hostID, slug))
}

func (s *Store) ListInstances(hostID string) ([]model.Instance, error) {
	q := `SELECT ` + instanceColumns + ` FROM instances`
	args := []any{}
	if hostID != "" {
		q += ` WHERE host_id = ?`
		args = append(args, hostID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Instance{}
	for rows.Next() {
		in, err := s.scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInstance(in *model.Instance) error {
	extra, err := json.Marshal(in.ExtraEnv)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE instances SET name=?, slug=?, base_dir=?, bind_address=?,
		tm_port=?, grafana_port=?, mqtt_port=?, timezone=?, region=?, image_tag=?, postgres_tag=?,
		grafana_admin_user=?, disable_mqtt=?, extra_env=?, source=?, tm_encryption_key=?, db_user=?, db_pass=?,
		grafana_pass=?, compose_major=?, compose_sha256=?, status=?, status_message=?, last_job_id=?,
		updated_at=? WHERE id=?`,
		in.Name, in.Slug, in.BaseDir, in.BindAddress, in.TMPort, in.GrafanaPort, in.MQTTPort,
		in.Timezone, in.Region, in.ImageTag, in.PostgresTag, in.GrafanaAdminUser,
		boolInt(in.DisableMQTT), string(extra), in.Source, in.TMEncryptionKey, in.DBUser, in.DBPass,
		in.GrafanaPass, in.ComposeMajor, in.ComposeSHA256, in.Status, in.StatusMessage,
		in.LastJobID, now(), in.ID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) SetInstanceStatus(id string, status model.InstanceStatus, message, jobID string) error {
	res, err := s.db.Exec(`UPDATE instances SET status=?, status_message=?, last_job_id=?, updated_at=? WHERE id=?`,
		status, message, jobID, now(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) DeleteInstance(id string) error {
	res, err := s.db.Exec(`DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// PortHolders returns the ports already claimed on a host so the caller can
// reject conflicts before asking the remote machine.
func (s *Store) PortHolders(hostID, excludeInstanceID string) (map[int]string, error) {
	rows, err := s.db.Query(`SELECT id, tm_port, grafana_port, mqtt_port FROM instances WHERE host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var id string
		var a, b, c int
		if err := rows.Scan(&id, &a, &b, &c); err != nil {
			return nil, err
		}
		if id == excludeInstanceID {
			continue
		}
		out[a] = id
		out[b] = id
		out[c] = id
	}
	return out, rows.Err()
}

// InstanceHostID resolves just the owning host, used by queue guards.
func (s *Store) InstanceHostID(id string) (string, error) {
	var hostID string
	err := s.db.QueryRow(`SELECT host_id FROM instances WHERE id = ?`, id).Scan(&hostID)
	if err != nil {
		return "", mapErr(err)
	}
	return hostID, nil
}

func nowTime() time.Time { return time.Now().UTC() }
