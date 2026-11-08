package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"1mate/internal/compose"
	"1mate/internal/model"
	"1mate/internal/sshx"
	"1mate/internal/store"
)

// DeployPayload is queued for deploy and upgrade jobs.
type DeployPayload struct {
	InstanceID string `json:"instance_id"`
}

// DestroyPayload is queued for the destroy job.
type DestroyPayload struct {
	InstanceID   string `json:"instance_id"`
	PurgeVolumes bool   `json:"purge_volumes"`
	ConfirmSlug  string `json:"confirm_slug"`
}

// SimplePayload carries just an instance id.
type SimplePayload struct {
	InstanceID string `json:"instance_id"`
}

func parseSimple(raw string) (SimplePayload, error) {
	var p SimplePayload
	if raw == "" || raw == "{}" {
		return p, nil
	}
	return p, json.Unmarshal([]byte(raw), &p)
}

// RunDeployJob renders the files, uploads them atomically, validates them with
// `compose config` and brings the stack up.
func (s *Service) RunDeployJob(ctx context.Context, payload string, logf func(string, ...any)) error {
	p, err := parseSimple(payload)
	if err != nil {
		return err
	}
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	_ = s.Store.SetInstanceStatus(inst.ID, model.InstDeploying, "deploying", "")

	if err := s.deploy(ctx, inst, logf); err != nil {
		_ = s.Store.SetInstanceStatus(inst.ID, model.InstFailed, err.Error(), "")
		return err
	}
	_ = s.Store.SetInstanceStatus(inst.ID, model.InstRunning, "", "")
	return nil
}

func (s *Service) deploy(ctx context.Context, inst *model.Instance, logf func(string, ...any)) error {
	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return err
	}
	defer r.Close()

	spec := s.specFor(inst)
	composeBody, err := compose.Render(spec, r.major)
	if err != nil {
		return err
	}
	envBody, err := compose.RenderEnv(spec)
	if err != nil {
		return err
	}
	sum := compose.Checksum(composeBody)

	if logf != nil {
		logf("rendering files for %s (compose v%d)", inst.ProjectName, r.major)
	}

	// Keep a copy of the previous revision so a failed deploy can be retried
	// against known-good content.
	prevCompose, prevEnv := "", ""
	if res, err := r.run(ctx, "true", 30*time.Second, nil); err == nil && res.ExitCode == 0 {
		prevCompose, _ = s.readRemoteFile(ctx, r, inst.BaseDir+"/compose.yaml")
		prevEnv, _ = s.readRemoteFile(ctx, r, inst.BaseDir+"/.env")
	}

	composePath := inst.BaseDir + "/compose.yaml"
	envPath := inst.BaseDir + "/.env"
	if err := r.client.Upload(ctx, composePath, composeBody, "0640"); err != nil {
		return fmt.Errorf("upload compose.yaml: %w", classifySSHError(err))
	}
	if err := r.client.Upload(ctx, envPath, envBody, "0600"); err != nil {
		return fmt.Errorf("upload .env: %w", classifySSHError(err))
	}

	if logf != nil {
		logf("validating the compose configuration")
	}
	if res, err := r.run(ctx, "config -q", 2*time.Minute, nil); err != nil {
		return classifySSHError(err)
	} else if res.ExitCode != 0 {
		s.restorePrevious(ctx, r, prevCompose, prevEnv, composePath, envPath)
		return fmt.Errorf("compose config rejected the generated file: %s", truncate(strings.TrimSpace(res.Stdout), 2000))
	}

	if logf != nil {
		logf("starting the stack")
	}
	res, err := r.run(ctx, "up -d --remove-orphans", 20*time.Minute, func(chunk string) {
		if logf != nil {
			for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
				if line != "" {
					logf("%s", line)
				}
			}
		}
	})
	if err != nil {
		return classifySSHError(err)
	}
	if res.ExitCode != 0 {
		s.restorePrevious(ctx, r, prevCompose, prevEnv, composePath, envPath)
		return fmt.Errorf("docker compose up failed (exit %d): %s", res.ExitCode, truncate(strings.TrimSpace(res.Stdout), 3000))
	}

	cp := *inst
	cp.ComposeSHA256 = sum
	cp.ComposeMajor = r.major
	if err := s.Store.UpdateInstance(&cp); err != nil {
		return err
	}
	if logf != nil {
		logf("stack %s is up", inst.ProjectName)
	}
	return nil
}

func (s *Service) restorePrevious(ctx context.Context, r *composeRunner, prevCompose, prevEnv, composePath, envPath string) {
	if prevCompose == "" {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = r.client.Upload(cctx, composePath, prevCompose, "0640")
	if prevEnv != "" {
		_ = r.client.Upload(cctx, envPath, prevEnv, "0600")
	}
}

func (s *Service) readRemoteFile(ctx context.Context, r *composeRunner, path string) (string, error) {
	res, err := r.client.Run(ctx, "cat -- "+sshx.Quote(path)+" 2>/dev/null", sshx.RunOptions{Timeout: 30 * time.Second, Sudo: r.sudo})
	if err != nil || res.ExitCode != 0 {
		return "", errors.New("unreadable")
	}
	return res.Stdout, nil
}

// RunSimpleAction handles start, stop, restart and reconcile.
func (s *Service) RunSimpleAction(ctx context.Context, kind model.JobKind, payload string, logf func(string, ...any)) error {
	p, err := parseSimple(payload)
	if err != nil {
		return err
	}
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return err
	}
	defer r.Close()

	stream := func(chunk string) {
		if logf == nil {
			return
		}
		for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
			if line != "" {
				logf("%s", line)
			}
		}
	}

	switch kind {
	case model.JobStart:
		if logf != nil {
			logf("starting %s", inst.ProjectName)
		}
		res, err := r.run(ctx, "up -d", 10*time.Minute, stream)
		if err != nil {
			return classifySSHError(err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("start failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 2000))
		}
		_ = s.Store.SetInstanceStatus(inst.ID, model.InstRunning, "", "")
	case model.JobStop:
		if logf != nil {
			logf("stopping %s", inst.ProjectName)
		}
		res, err := r.run(ctx, "stop", 5*time.Minute, stream)
		if err != nil {
			return classifySSHError(err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("stop failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 2000))
		}
		_ = s.Store.SetInstanceStatus(inst.ID, model.InstStopped, "stopped by user", "")
	case model.JobRestart:
		if logf != nil {
			logf("restarting %s", inst.ProjectName)
		}
		res, err := r.run(ctx, "restart", 10*time.Minute, stream)
		if err != nil {
			return classifySSHError(err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("restart failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 2000))
		}
		_ = s.Store.SetInstanceStatus(inst.ID, model.InstRunning, "", "")
	case model.JobReconcile:
		return s.reconcile(ctx, r, inst, logf)
	default:
		return fmt.Errorf("unsupported action %s", kind)
	}
	return nil
}

// reconcile compares the platform record with the remote containers and flags
// file drift.
func (s *Service) reconcile(ctx context.Context, r *composeRunner, inst *model.Instance, logf func(string, ...any)) error {
	res, err := r.run(ctx, "ps --format json", 2*time.Minute, nil)
	if err != nil {
		return classifySSHError(err)
	}
	states := parseComposePS(res.Stdout, r.major)
	for name, state := range states {
		if logf != nil {
			logf("%s: %s", name, state)
		}
	}
	status := aggregateStatus(states)
	if logf != nil {
		logf("aggregated instance state: %s", status)
	}

	remoteCompose, err := s.readRemoteFile(ctx, r, inst.BaseDir+"/compose.yaml")
	if err == nil && inst.ComposeSHA256 != "" {
		if compose.Checksum(remoteCompose) != inst.ComposeSHA256 {
			if logf != nil {
				logf("WARNING: the remote compose.yaml differs from the deployed revision; redeploy to converge")
			}
			_ = s.Store.SetInstanceStatus(inst.ID, status, "remote compose.yaml drifted from the platform record", "")
			return nil
		}
	}
	_ = s.Store.SetInstanceStatus(inst.ID, status, "", "")
	return nil
}

// parseComposePS understands both the v2 JSON array and the legacy
// newline-delimited JSON emitted by older Compose builds.
func parseComposePS(out string, major int) map[string]string {
	states := map[string]string{}
	out = strings.TrimSpace(out)
	if out == "" {
		return states
	}
	type row struct {
		Service string `json:"Service"`
		Name    string `json:"Name"`
		State   string `json:"State"`
		Status  string `json:"Status"`
	}
	var rows []row
	if strings.HasPrefix(out, "[") {
		_ = json.Unmarshal([]byte(out), &rows)
	} else {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var one row
			if err := json.Unmarshal([]byte(line), &one); err == nil {
				rows = append(rows, one)
			}
		}
	}
	for _, r := range rows {
		key := r.Service
		if key == "" {
			key = r.Name
		}
		state := strings.ToLower(r.State)
		if state == "" {
			// Legacy format only has a status string like "Up 2 minutes".
			if strings.HasPrefix(strings.ToLower(r.Status), "up") {
				state = "running"
			}
		}
		if key != "" {
			states[key] = state
		}
	}
	return states
}

func aggregateStatus(states map[string]string) model.InstanceStatus {
	if len(states) == 0 {
		return model.InstUnknown
	}
	running, stopped := 0, 0
	for _, st := range states {
		switch st {
		case "running":
			running++
		case "exited", "stopped", "created":
			stopped++
		}
	}
	switch {
	case running == len(states):
		return model.InstRunning
	case stopped == len(states):
		return model.InstStopped
	case running > 0:
		return model.InstDegraded
	}
	return model.InstUnknown
}

// RunUpgradeJob backs up, pulls images and restarts. Postgres major upgrades are
// refused because they need an explicit dump/restore.
func (s *Service) RunUpgradeJob(ctx context.Context, payload string, logf func(string, ...any)) error {
	p, err := parseSimple(payload)
	if err != nil {
		return err
	}
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return err
	}
	defer r.Close()

	if logf != nil {
		logf("taking a safety backup before upgrading")
	}
	if err := s.RunBackupJob(ctx, BackupPayload{InstanceID: inst.ID, Note: "pre-upgrade safety backup"}, logf); err != nil {
		return fmt.Errorf("upgrade aborted: safety backup failed: %w", err)
	}

	stream := func(chunk string) {
		if logf == nil {
			return
		}
		for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
			if line != "" {
				logf("%s", line)
			}
		}
	}

	if logf != nil {
		logf("pulling images")
	}
	res, err := r.run(ctx, "pull", 30*time.Minute, stream)
	if err != nil {
		return classifySSHError(err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("image pull failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 3000))
	}

	if logf != nil {
		logf("recreating containers")
	}
	res, err = r.run(ctx, "up -d --remove-orphans", 20*time.Minute, stream)
	if err != nil {
		return classifySSHError(err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("upgrade restart failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 3000))
	}
	_ = s.Store.SetInstanceStatus(inst.ID, model.InstRunning, "upgraded", "")
	return nil
}

// RunDestroyJob removes the stack. Volumes are only purged with an explicit
// confirmation of the slug.
func (s *Service) RunDestroyJob(ctx context.Context, payload string, logf func(string, ...any)) error {
	var p DestroyPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return err
	}
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	if p.PurgeVolumes && p.ConfirmSlug != inst.Slug {
		return fmt.Errorf("%w: confirm_slug must match %q to delete data volumes", ErrValidation, inst.Slug)
	}

	r, err := s.newRunner(ctx, inst)
	if err != nil {
		// A host that is gone should still let the operator clean up the record.
		if errors.Is(err, store.ErrNotFound) {
			return s.DeleteInstance(inst.ID)
		}
		if logf != nil {
			logf("cannot reach the host (%v); removing the record only", err)
		}
		return s.DeleteInstance(inst.ID)
	}
	defer r.Close()

	args := "down --remove-orphans"
	if p.PurgeVolumes {
		args += " -v"
	}
	if logf != nil {
		logf("bringing the stack down (%s)", args)
	}
	res, err := r.run(ctx, args, 10*time.Minute, nil)
	if err != nil {
		return classifySSHError(err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("docker compose down failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 2000))
	}
	if p.PurgeVolumes {
		if logf != nil {
			logf("removing the instance directory %s", inst.BaseDir)
		}
		res, err := r.client.Run(ctx, "rm -rf -- "+sshx.Quote(inst.BaseDir), sshx.RunOptions{Timeout: 2 * time.Minute, Sudo: r.sudo})
		if err != nil {
			return classifySSHError(err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("removing the instance directory failed (exit %d): %s", res.ExitCode, truncate(res.Stdout, 1000))
		}
	}
	if logf != nil {
		logf("removing the instance record")
	}
	return s.DeleteInstance(inst.ID)
}

// ProbeInstance health runs a lightweight check used by the background poller.
func (s *Service) ProbeInstance(ctx context.Context, inst *model.Instance) (model.InstanceStatus, string, error) {
	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return model.InstUnknown, err.Error(), err
	}
	defer r.Close()
	res, err := r.run(ctx, "ps --format json", 60*time.Second, nil)
	if err != nil {
		return model.InstUnknown, err.Error(), err
	}
	states := parseComposePS(res.Stdout, r.major)
	status := aggregateStatus(states)
	msg := ""
	if status == model.InstUnknown {
		msg = "no containers reported by compose"
	}
	return status, msg, nil
}

// UpgradeGuard rejects a Postgres major version change.
func UpgradeGuard(oldTag, newTag string) error {
	oldMajor := pgMajor(oldTag)
	newMajor := pgMajor(newTag)
	if oldMajor != 0 && newMajor != 0 && oldMajor != newMajor {
		return fmt.Errorf("%w: changing PostgreSQL from %d to %d requires a manual dump and restore; create a new instance with the new tag and restore the backup", ErrValidation, oldMajor, newMajor)
	}
	return nil
}

func pgMajor(tag string) int {
	tag = strings.TrimSpace(tag)
	digits := ""
	for _, r := range tag {
		if r >= '0' && r <= '9' {
			digits += string(r)
			continue
		}
		break
	}
	n, _ := strconv.Atoi(digits)
	return n
}
