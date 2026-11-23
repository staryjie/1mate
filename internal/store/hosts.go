package store

import (
	"database/sql"
	"time"

	"1mate/internal/model"
)

const hostColumns = `id, name, address, ssh_port, ssh_user, auth_type, credential_id, host_key_fingerprint,
	sudo_mode, sudo_credential_id, connection_type, agent_token, agent_id, status, status_message, last_probe_at, created_at, updated_at`

func (s *Store) scanHost(sc interface{ Scan(...any) error }) (*model.Host, error) {
	var h model.Host
	var lastProbe sql.NullString
	var created, updated string
	if err := sc.Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.AuthType, &h.CredentialID,
		&h.HostKeyFingerpr, &h.SudoMode, &h.SudoCredentialID, &h.ConnectionType, &h.AgentToken, &h.AgentID,
		&h.Status, &h.StatusMessage, &lastProbe,
		&created, &updated); err != nil {
		return nil, mapErr(err)
	}
	h.LastProbeAt = parseTimePtr(lastProbe)
	h.CreatedAt = parseTime(created)
	h.UpdatedAt = parseTime(updated)
	h.HasCredential = h.CredentialID != ""
	h.HasSudoPass = h.SudoCredentialID != ""
	h.AgentTokenSet = h.AgentToken != ""
	return &h, nil
}

func (s *Store) CreateHost(h *model.Host) error {
	ts := now()
	_, err := s.db.Exec(`INSERT INTO hosts(`+hostColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.Name, h.Address, h.SSHPort, h.SSHUser, h.AuthType, h.CredentialID, h.HostKeyFingerpr,
		h.SudoMode, h.SudoCredentialID, h.ConnectionType, h.AgentToken, h.AgentID, h.Status, h.StatusMessage, nil, ts, ts)
	if err != nil {
		return err
	}
	h.CreatedAt, h.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) GetHost(id string) (*model.Host, error) {
	return s.scanHost(s.db.QueryRow(`SELECT `+hostColumns+` FROM hosts WHERE id = ?`, id))
}

func (s *Store) ListHosts() ([]model.Host, error) {
	rows, err := s.db.Query(`SELECT ` + hostColumns + ` FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Host{}
	for rows.Next() {
		h, err := s.scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (s *Store) UpdateHost(h *model.Host) error {
	res, err := s.db.Exec(`UPDATE hosts SET name=?, address=?, ssh_port=?, ssh_user=?, auth_type=?,
		credential_id=?, host_key_fingerprint=?, sudo_mode=?, sudo_credential_id=?, connection_type=?,
		agent_token=?, agent_id=?, status=?, status_message=?, updated_at=? WHERE id=?`,
		h.Name, h.Address, h.SSHPort, h.SSHUser, h.AuthType, h.CredentialID, h.HostKeyFingerpr,
		h.SudoMode, h.SudoCredentialID, h.ConnectionType, h.AgentToken, h.AgentID, h.Status, h.StatusMessage, now(), h.ID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) SetHostProbeResult(id string, status model.HostStatus, message string, fingerprint string) error {
	q := `UPDATE hosts SET status=?, status_message=?, last_probe_at=?, updated_at=?`
	args := []any{status, message, now(), now()}
	if fingerprint != "" {
		q = `UPDATE hosts SET status=?, status_message=?, host_key_fingerprint=?, last_probe_at=?, updated_at=?`
		args = []any{status, message, fingerprint, now(), now()}
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) SetHostStatus(id string, status model.HostStatus, message string) error {
	res, err := s.db.Exec(`UPDATE hosts SET status=?, status_message=?, updated_at=? WHERE id=?`,
		status, message, now(), id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) DeleteHost(id string) error {
	res, err := s.db.Exec(`DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) CountInstancesForHost(hostID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM instances WHERE host_id = ?`, hostID).Scan(&n)
	return n, err
}

func (s *Store) SaveCapabilities(c *model.Capabilities) error {
	_, err := s.db.Exec(`INSERT INTO host_capabilities(
		host_id, os_id, os_version, os_codename, os_pretty, arch, kernel, init_system, cgroup_version,
		cpu_count, mem_mb, disk_free_kb, docker_present, docker_client_version, docker_server_version,
		docker_running, docker_socket_access, docker_sudo, docker_rootless, compose_kind, compose_version,
		compose_major, compose_path, compose_v1_binary_present, ports_in_use, unsupported_reason, raw_json, probed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(host_id) DO UPDATE SET
			os_id=excluded.os_id, os_version=excluded.os_version, os_codename=excluded.os_codename,
			os_pretty=excluded.os_pretty, arch=excluded.arch, kernel=excluded.kernel,
			init_system=excluded.init_system, cgroup_version=excluded.cgroup_version,
			cpu_count=excluded.cpu_count, mem_mb=excluded.mem_mb, disk_free_kb=excluded.disk_free_kb,
			docker_present=excluded.docker_present, docker_client_version=excluded.docker_client_version,
			docker_server_version=excluded.docker_server_version, docker_running=excluded.docker_running,
			docker_socket_access=excluded.docker_socket_access, docker_sudo=excluded.docker_sudo,
			docker_rootless=excluded.docker_rootless, compose_kind=excluded.compose_kind,
			compose_version=excluded.compose_version, compose_major=excluded.compose_major,
			compose_path=excluded.compose_path, compose_v1_binary_present=excluded.compose_v1_binary_present,
			ports_in_use=excluded.ports_in_use, unsupported_reason=excluded.unsupported_reason,
			raw_json=excluded.raw_json, probed_at=excluded.probed_at`,
		c.HostID, c.OSID, c.OSVersion, c.OSCodename, c.OSPretty, c.Arch, c.Kernel, c.InitSystem,
		c.Cgroup, c.CPUCount, c.MemMB, c.DiskFreeKB, boolInt(c.DockerPresent), c.DockerClient,
		c.DockerServer, boolInt(c.DockerRunning), c.SocketAccess, boolInt(c.DockerSudo),
		boolInt(c.DockerRootless), c.ComposeKind, c.ComposeVersion, c.ComposeMajor, c.ComposePath,
		boolInt(c.ComposeV1Present), intsJSON(c.PortsInUse), c.UnsupportedReason, c.RawJSON,
		c.ProbedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetCapabilities(hostID string) (*model.Capabilities, error) {
	var c model.Capabilities
	var dockerPresent, dockerRunning, dockerSudo, dockerRootless, v1Present int
	var ports, probedAt string
	err := s.db.QueryRow(`SELECT host_id, os_id, os_version, os_codename, os_pretty, arch, kernel,
		init_system, cgroup_version, cpu_count, mem_mb, disk_free_kb, docker_present, docker_client_version,
		docker_server_version, docker_running, docker_socket_access, docker_sudo, docker_rootless,
		compose_kind, compose_version, compose_major, compose_path, compose_v1_binary_present, ports_in_use,
		unsupported_reason, raw_json, probed_at FROM host_capabilities WHERE host_id = ?`, hostID).
		Scan(&c.HostID, &c.OSID, &c.OSVersion, &c.OSCodename, &c.OSPretty, &c.Arch, &c.Kernel,
			&c.InitSystem, &c.Cgroup, &c.CPUCount, &c.MemMB, &c.DiskFreeKB, &dockerPresent,
			&c.DockerClient, &c.DockerServer, &dockerRunning, &c.SocketAccess, &dockerSudo,
			&dockerRootless, &c.ComposeKind, &c.ComposeVersion, &c.ComposeMajor, &c.ComposePath,
			&v1Present, &ports, &c.UnsupportedReason, &c.RawJSON, &probedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	c.DockerPresent = dockerPresent != 0
	c.DockerRunning = dockerRunning != 0
	c.DockerSudo = dockerSudo != 0
	c.DockerRootless = dockerRootless != 0
	c.ComposeV1Present = v1Present != 0
	c.PortsInUse = parseIntsJSON(ports)
	c.ProbedAt = parseTime(probedAt)
	return &c, nil
}

func rowsAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
