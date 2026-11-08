package api

import (
	"net/http"
)

// handleBackupUpload accepts a dump file for a restore.
func (s *Server) handleBackupUpload(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	inst, err := s.svc.GetInstance(instanceID)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	_ = inst
	r.Body = http.MaxBytesReader(w, r.Body, 8<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.fail(w, http.StatusBadRequest, CodeBadRequest, "expected a multipart upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, http.StatusBadRequest, CodeBadRequest, "the file field is required")
		return
	}
	defer file.Close()
	b, err := s.svc.ImportBackupFile(instanceID, header.Filename, file, r.FormValue("note"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "backup.upload", "instance", instanceID, map[string]any{
		"backup_id": b.ID, "filename": b.Filename, "size": b.SizeBytes,
	}, r)
	writeJSON(w, http.StatusCreated, b)
}

// handleListBackups lists stored dumps for an instance.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListBackups(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": list})
}

// handleDownloadBackup streams a dump to the caller.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBackup(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	path := s.svc.BackupPath(b)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.Filename+`"`)
	s.audit.Log(s.actor(r), "backup.download", "instance", b.InstanceID, map[string]any{"backup_id": b.ID}, r)
	http.ServeFile(w, r, path)
}

// handleDeleteBackup removes a stored dump.
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBackup(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.svc.RemoveBackupFile(b)
	if err := s.store.DeleteBackup(b.ID); err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "backup.delete", "instance", b.InstanceID, map[string]any{"backup_id": b.ID}, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
