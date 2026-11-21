package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/sshx"
)

// BackupPayload is queued for the backup job.
type BackupPayload struct {
	InstanceID string `json:"instance_id"`
	Note       string `json:"note"`
}

// RestorePayload is queued for the restore job.
type RestorePayload struct {
	InstanceID     string `json:"instance_id"`
	BackupID       string `json:"backup_id"`
	UploadFilename string `json:"upload_filename"`
}

// BackupDir is where dump files are stored on the platform.
func (s *Service) BackupDir(instanceID string) string {
	return filepath.Join(s.DataDir, "backups", instanceID)
}

func backupFileName(now time.Time) string {
	return "teslamate-" + now.UTC().Format("20060102-150405") + ".dump"
}

var backupNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// RunBackupJob streams a custom-format pg_dump from the remote database into
// the platform's backup directory.
func (s *Service) RunBackupJob(ctx context.Context, p BackupPayload, logf func(string, ...any)) error {
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	// Backward-compatible: instances imported/created before DBName was added
	// may have an empty DBName. Fall back to slug-derived name (teslamate_<slug>).
	if inst.DBName == "" {
		inst.DBName = "teslamate_" + strings.ReplaceAll(inst.Slug, "-", "_")
	}
	dir := s.BackupDir(inst.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := backupFileName(time.Now())
	finalPath := filepath.Join(dir, name)
	tmpPath := finalPath + ".partial"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	record := &model.Backup{
		ID:         ids.New(),
		InstanceID: inst.ID,
		Filename:   name,
		Format:     "fc",
		Note:       p.Note,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.Store.CreateBackup(record); err != nil {
		os.Remove(tmpPath)
		return err
	}

	r, err := s.newRunner(ctx, inst)
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	defer r.Close()

	if logf != nil {
		logf("dumping database %s from %s as custom format", inst.DBName, inst.ProjectName)
	}
	cmd := r.cmd("exec -T database pg_dump -U " + sshx.Quote(inst.DBUser) + " -Fc " + sshx.Quote(inst.DBName))
	hasher := sha256.New()
	mw := io.MultiWriter(f, hasher)
	n, err := r.client.DownloadWithCommand(ctx, cmd, sshx.RunOptions{Timeout: 2 * time.Hour, Sudo: r.sudo}, mw)
	if err != nil {
		os.Remove(tmpPath)
		_ = s.Store.DeleteBackup(record.ID)
		return fmt.Errorf("backup failed: %w", err)
	}
	if err := f.Sync(); err != nil {
		os.Remove(tmpPath)
		_ = s.Store.DeleteBackup(record.ID)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		_ = s.Store.DeleteBackup(record.ID)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		_ = s.Store.DeleteBackup(record.ID)
		return err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	record.SizeBytes = n
	record.SHA256 = sum
	if err := s.Store.UpdateBackupSize(record.ID, n, sum); err != nil {
		return err
	}
	if logf != nil {
		logf("backup stored as %s (%d bytes, sha256 %s)", name, n, sum[:12])
	}
	return nil
}

// BackupPath resolves a stored dump to an absolute path.
func (s *Service) BackupPath(b *model.Backup) string {
	return filepath.Join(s.BackupDir(b.InstanceID), b.Filename)
}

// RemoveBackupFile deletes the dump from disk; missing files are ignored.
func (s *Service) RemoveBackupFile(b *model.Backup) {
	if b == nil || !backupNamePattern.MatchString(b.Filename) {
		return
	}
	_ = os.Remove(s.BackupPath(b))
}

// ImportBackupFile records an uploaded dump, verifying its checksum.
func (s *Service) ImportBackupFile(instanceID, filename string, src io.Reader, note string) (*model.Backup, error) {
	inst, err := s.Store.GetInstance(instanceID)
	if err != nil {
		return nil, err
	}
	if !backupNamePattern.MatchString(filename) {
		return nil, fmt.Errorf("%w: filename may only contain letters, digits, dot, dash and underscore", ErrValidation)
	}
	if !strings.HasSuffix(filename, ".dump") && !strings.HasSuffix(filename, ".bck") {
		return nil, fmt.Errorf("%w: the file must end with .dump or .bck", ErrValidation)
	}
	dir := s.BackupDir(inst.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	finalPath := filepath.Join(dir, "upload-"+filename)
	tmp := finalPath + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hasher), src)
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		os.Remove(tmp)
		return nil, copyErr
	}
	if n == 0 {
		os.Remove(tmp)
		return nil, fmt.Errorf("%w: the uploaded file is empty", ErrValidation)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	b := &model.Backup{
		ID:         ids.New(),
		InstanceID: inst.ID,
		Filename:   "upload-" + filename,
		SizeBytes:  n,
		SHA256:     hex.EncodeToString(hasher.Sum(nil)),
		Format:     "fc",
		Note:       note,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.Store.CreateBackup(b); err != nil {
		os.Remove(finalPath)
		return nil, err
	}
	return b, nil
}

// RunRestoreJob restores a dump: stop teslamate, take a safety backup, restore,
// then start again.
func (s *Service) RunRestoreJob(ctx context.Context, p RestorePayload, logf func(string, ...any)) error {
	inst, err := s.GetInstance(p.InstanceID)
	if err != nil {
		return err
	}
	b, err := s.Store.GetBackup(p.BackupID)
	if err != nil {
		return err
	}
	if b.InstanceID != inst.ID {
		return fmt.Errorf("%w: backup does not belong to this instance", ErrValidation)
	}
	path := s.BackupPath(b)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backup file missing: %w", err)
	}
	defer f.Close()
	if b.SHA256 != "" {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
			return fmt.Errorf("backup checksum mismatch: file is corrupted")
		}
	}

	// Always take a fresh backup before overwriting the live database.
	if logf != nil {
		logf("taking a safety backup before restoring")
	}
	if err := s.RunBackupJob(ctx, BackupPayload{InstanceID: inst.ID, Note: "pre-restore safety backup"}, logf); err != nil {
		return fmt.Errorf("safety backup failed, restore aborted: %w", err)
	}

	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return err
	}
	defer r.Close()

	if logf != nil {
		logf("stopping the teslamate container")
	}
	stopRes, err := r.run(ctx, "stop teslamate", 5*time.Minute, nil)
	if err != nil {
		return classifySSHError(err)
	}
	if stopRes.ExitCode != 0 {
		return fmt.Errorf("cannot stop teslamate (exit %d): %s", stopRes.ExitCode, strings.TrimSpace(stopRes.Stdout))
	}

	remotePath := inst.BaseDir + "/import/restore-" + ids.New()[:8] + ".dump"
	if logf != nil {
		logf("uploading backup to %s (%d bytes)", remotePath, b.SizeBytes)
	}
	// Upload in a streaming fashion to avoid holding the dump in memory.
	if err := r.client.UploadReader(ctx, remotePath, f, "0600", sshx.RunOptions{Sudo: r.sudo}); err != nil {
		return fmt.Errorf("upload failed: %w", classifySSHError(err))
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = r.client.Run(cctx, "rm -f "+sshx.Quote(remotePath), sshx.RunOptions{Timeout: 20 * time.Second, Sudo: r.sudo})
	}()

	restoreCmd := fmt.Sprintf("exec -T database pg_restore --clean --if-exists --no-owner -U %s -d %s < %s",
		sshx.Quote(inst.DBUser), sshx.Quote(inst.DBName), sshx.Quote(remotePath))
	if logf != nil {
		logf("restoring the database")
	}
	res, err := r.run(ctx, restoreCmd, 2*time.Hour, nil)
	if err != nil {
		return classifySSHError(err)
	}
	// pg_restore warns about missing objects with --clean; only a failure to
	// connect is fatal, which surfaces as a non-zero exit with no restore output.
	if res.ExitCode != 0 {
		out := strings.TrimSpace(res.Stdout)
		if !strings.Contains(out, "errors ignored on restore") && !strings.Contains(out, "WARNING") {
			return fmt.Errorf("pg_restore failed (exit %d): %s", res.ExitCode, truncate(out, 2000))
		}
		if logf != nil {
			logf("pg_restore finished with warnings: %s", truncate(out, 1000))
		}
	}

	if logf != nil {
		logf("starting teslamate again")
	}
	startRes, err := r.run(ctx, "up -d", 10*time.Minute, nil)
	if err != nil {
		return classifySSHError(err)
	}
	if startRes.ExitCode != 0 {
		return fmt.Errorf("restore finished but the stack did not start (exit %d): %s", startRes.ExitCode, truncate(startRes.Stdout, 2000))
	}
	_ = s.Store.SetInstanceStatus(inst.ID, model.InstRunning, "restored from "+b.Filename, "")
	return nil
}
