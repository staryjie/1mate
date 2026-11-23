package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"1mate/internal/cryptox"
	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/redact"
	"1mate/internal/sshx"
	"1mate/internal/store"
)

// harness wires a Service against a temporary database and a fake SSH client.
type harness struct {
	svc   *Service
	store *store.Store
	fake  *sshx.Fake
	host  *model.Host
	mu    sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, cryptox.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := cryptox.NewBox(key)
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	red := redact.New()
	agentHub := NewAgentHub(slog.Default())
	svc := New(st, box, red, dir, slog.Default(), agentHub)
	fake := sshx.NewFake("SHA256:test")
	svc.Dial = func(addr string, port int, auth sshx.Auth) sshx.Client { return fake }
	return &harness{svc: svc, store: st, fake: fake}
}

// createHost stores a host through the public API path so credentials are
// encrypted exactly as production does.
func (h *harness) createHost(t *testing.T, sudoMode model.SudoMode) *model.Host {
	t.Helper()
	host, err := h.svc.CreateHost(HostInput{
		Name:       "test-host",
		Address:    "10.0.0.10",
		SSHPort:    22,
		SSHUser:    "deploy",
		AuthType:   model.HostAuthKey,
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----",
		SudoMode:   sudoMode,
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	h.host = host
	return host
}

// setProbeOutput makes the fake answer the probe script with the given output.
// The probe is executed as `sh /tmp/1mate-probe-<random>.sh`, so the fake
// detects that command and returns the scripted output.
func (h *harness) setProbeOutput(t *testing.T, output string) {
	t.Helper()
	h.fake.Handler = func(cmd string) (sshx.Result, error) {
		if strings.Contains(cmd, "1mate-probe-") {
			return sshx.Result{ExitCode: 0, Stdout: output}, nil
		}
		if strings.Contains(cmd, "cat --") {
			return sshx.Result{ExitCode: 1}, nil
		}
		return sshx.Result{ExitCode: 0}, nil
	}
}

func (h *harness) probe(t *testing.T) *model.Capabilities {
	t.Helper()
	h.setProbeOutput(t, deadProbe)
	caps, err := h.svc.ProbeHost(context.Background(), h.host, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return caps
}

const probeV2 = `IM_OS_ID=ubuntu
IM_OS_VERSION=24.04
IM_OS_CODENAME=noble
IM_OS_PRETTY=Ubuntu 24.04 LTS
IM_ARCH=x86_64
IM_KERNEL=6.8.0
IM_CPUS=2
IM_MEM_MB=4000
IM_DISK_FREE_KB=20000000
IM_INIT=systemd
IM_CGROUP=2
IM_SUDO=nopasswd
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_CLIENT=27.3.1
IM_DOCKER_INFO=ok
IM_DOCKER_SERVER=27.3.1
IM_DOCKER_SUDO=0
IM_DOCKER_ROOTLESS=0
IM_COMPOSE_KIND=plugin_v2
IM_COMPOSE_VERSION=2.29.7
IM_COMPOSE_V1_PRESENT=0
IM_LISTEN_PORTS=22
`

const probeV1 = `IM_OS_ID=debian
IM_OS_VERSION=12
IM_ARCH=x86_64
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_INFO=ok
IM_DOCKER_SERVER=24.0.7
IM_COMPOSE_KIND=standalone_v1
IM_COMPOSE_VERSION=1.29.2
IM_COMPOSE_V1_PRESENT=1
IM_LISTEN_PORTS=22
`

const deadProbe = `IM_OS_ID=debian
IM_OS_VERSION=12
IM_ARCH=x86_64
IM_DOCKER_PATH=
IM_DOCKER_INFO=absent
IM_COMPOSE_KIND=none
IM_LISTEN_PORTS=
`

func TestProbeStoresCapabilitiesAndStatus(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, probeV2)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := h.store.GetHost(h.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.HostOK {
		t.Fatalf("status=%s message=%s", stored.Status, stored.StatusMessage)
	}
	if stored.HostKeyFingerpr != "SHA256:test" {
		t.Fatalf("fingerprint not pinned: %q", stored.HostKeyFingerpr)
	}
	caps, err := h.store.GetCapabilities(h.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if caps.ComposeKind != model.ComposePluginV2 || caps.ComposeMajor != 2 {
		t.Fatalf("compose: %+v", caps)
	}
}

func TestProbeHostWithoutDockerReportsNoDocker(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, deadProbe)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.store.GetHost(h.host.ID)
	if stored.Status != model.HostNoDocker {
		t.Fatalf("status=%s", stored.Status)
	}
}

func TestComposeCommandFollowsDetectedVersion(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
		kind   model.ComposeKind
	}{
		{"plugin v2", probeV2, "docker compose", model.ComposePluginV2},
		{"standalone v1", probeV1, "docker-compose", model.ComposeStandaloneV1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.createHost(t, model.SudoNopasswd)
			h.setProbeOutput(t, tc.output)
			if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
				t.Fatal(err)
			}
			cmd, kind, err := h.svc.ComposeCommandFor(h.host.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if cmd != tc.want || kind != tc.kind {
				t.Fatalf("got cmd=%q kind=%s", cmd, kind)
			}
		})
	}
}

func TestRequireV2BlocksComposeV1(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, probeV1)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.ComposeCommandFor(h.host.ID, true); err == nil {
		t.Fatal("expected v1 to be blocked when v2 is required")
	}
}

func TestCreateInstancePicksComposeMajorAndRejectsPorts(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, probeV1)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	inst, err := h.svc.CreateInstance(InstanceInput{
		Name: "Car One", Slug: "car-one", HostID: h.host.ID,
		TMPort: 4000, GrafanaPort: 3000, MQTTPort: 1883, Timezone: "Asia/Shanghai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ComposeMajor != 1 {
		t.Fatalf("instance must lock to compose v1, got %d", inst.ComposeMajor)
	}
	// Secrets must round-trip through encryption.
	loaded, err := h.svc.GetInstance(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.TMEncryptionKey) < 20 || len(loaded.DBPass) < 20 {
		t.Fatal("secrets were not generated")
	}
	// A duplicate port on the same host must be rejected.
	_, err = h.svc.CreateInstance(InstanceInput{
		Name: "Car Two", Slug: "car-two", HostID: h.host.ID,
		TMPort: 4000, GrafanaPort: 3001, MQTTPort: 1884, Timezone: "UTC",
	})
	if err == nil {
		t.Fatal("expected a port conflict")
	}
}

func TestDeployUsesVersionSpecificCommand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   string
		major  int
	}{
		{"v2", probeV2, "docker compose", 2},
		{"v1", probeV1, "docker-compose", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.createHost(t, model.SudoNopasswd)
			h.setProbeOutput(t, tc.output)
			if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
				t.Fatal(err)
			}
			inst, err := h.svc.CreateInstance(InstanceInput{
				Name: "Car", Slug: "car", HostID: h.host.ID,
				TMPort: 4000, GrafanaPort: 3000, MQTTPort: 1883, Timezone: "UTC",
			})
			if err != nil {
				t.Fatal(err)
			}
			h.fake.Calls = nil
			if err := h.svc.RunDeployJob(context.Background(), fmt.Sprintf(`{"instance_id":%q}`, inst.ID), nil); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			joined := strings.Join(h.fake.Calls, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("expected %q in commands:\n%s", tc.want, joined)
			}
			if tc.major == 1 && strings.Contains(joined, "docker compose ") {
				t.Fatalf("v1 host must not use the v2 command:\n%s", joined)
			}
			uploaded, ok := h.fake.Uploads["/opt/1mate/instances/car/compose.yaml"]
			if !ok {
				t.Fatal("compose file was not uploaded")
			}
			if tc.major == 1 && !strings.Contains(uploaded, `version: "3.9"`) {
				t.Fatalf("v1 instance must get a legacy compose file:\n%s", uploaded)
			}
			if tc.major == 2 && !strings.Contains(uploaded, "name: 1mate-car") {
				t.Fatalf("v2 instance must declare a project name:\n%s", uploaded)
			}
			if env, ok := h.fake.Uploads["/opt/1mate/instances/car/.env"]; !ok || !strings.Contains(env, "TM_DB_PASS=") {
				t.Fatal("env file with secrets was not uploaded")
			}
		})
	}
}

func TestBackupWritesChecksum(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, probeV2)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	inst, err := h.svc.CreateInstance(InstanceInput{
		Name: "Car", Slug: "car", HostID: h.host.ID,
		TMPort: 4000, GrafanaPort: 3000, MQTTPort: 1883, Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.DownloadData = []byte("PGDMP-fake-dump")
	if err := h.svc.RunBackupJob(context.Background(), BackupPayload{InstanceID: inst.ID, Note: "test"}, nil); err != nil {
		t.Fatalf("backup: %v", err)
	}
	list, err := h.store.ListBackups(inst.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("backups: %v %d", err, len(list))
	}
	if list[0].SHA256 == "" || list[0].SizeBytes != int64(len("PGDMP-fake-dump")) {
		t.Fatalf("backup record incomplete: %+v", list[0])
	}
}

func TestDestroyRequiresSlugForVolumePurge(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	h.setProbeOutput(t, probeV2)
	if _, err := h.svc.ProbeHost(context.Background(), h.host, nil); err != nil {
		t.Fatal(err)
	}
	inst, err := h.svc.CreateInstance(InstanceInput{
		Name: "Car", Slug: "car", HostID: h.host.ID,
		TMPort: 4000, GrafanaPort: 3000, MQTTPort: 1883, Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = h.svc.RunDestroyJob(context.Background(),
		fmt.Sprintf(`{"instance_id":%q,"purge_volumes":true,"confirm_slug":"wrong"}`, inst.ID), nil)
	if err == nil {
		t.Fatal("expected confirmation failure")
	}
	if _, err := h.store.GetInstance(inst.ID); err != nil {
		t.Fatal("instance must survive a refused destroy")
	}
}

func TestUpgradeGuardBlocksPostgresMajorChange(t *testing.T) {
	if err := UpgradeGuard("18-trixie", "18-bookworm"); err != nil {
		t.Fatalf("same major should pass: %v", err)
	}
	if err := UpgradeGuard("17-bookworm", "18-trixie"); err == nil {
		t.Fatal("major change must be blocked")
	}
}

func TestInstallScriptUsesPinnedComposeAndMirror(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNopasswd)
	body, opts, err := h.svc.InstallScript(h.host.ID, InstallPayload{Strategy: "repo", Mirror: "Aliyun"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ComposeVersion != model.DefaultComposeVersion {
		t.Fatalf("compose version=%q", opts.ComposeVersion)
	}
	if !strings.Contains(body, "mirrors.aliyun.com/docker-ce") {
		t.Fatal("mirror not applied")
	}
	if !strings.Contains(body, "docker-compose-plugin") {
		t.Fatal("v2 plugin not requested")
	}
}

func TestInstallJobRefusesWithoutPrivilege(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoNone)
	h.fake.Handler = func(cmd string) (sshx.Result, error) {
		if strings.HasPrefix(cmd, "id -u") {
			return sshx.Result{ExitCode: 0, Stdout: "1000\n"}, nil
		}
		return sshx.Result{ExitCode: 0}, nil
	}
	err := h.svc.RunInstallJob(context.Background(), InstallPayload{
		HostID: h.host.ID, Strategy: "repo", AcceptSystemChanges: true,
	}, nil)
	if err == nil {
		t.Fatal("expected a privilege error")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, _ := h.store.GetHost(h.host.ID)
	if stored.Status != model.HostDenied {
		t.Fatalf("status=%s", stored.Status)
	}
}

func TestInstallJobReportsStageFailure(t *testing.T) {
	h := newHarness(t)
	h.createHost(t, model.SudoRoot)
	h.fake.Handler = func(cmd string) (sshx.Result, error) {
		if strings.Contains(cmd, "1mate-install") {
			return sshx.Result{
				ExitCode: 11,
				Stdout:   "1MATE_STAGE=detect\n1MATE_ERROR=E_UNSUPPORTED_DISTRO: Alpine is not supported\n",
			}, nil
		}
		return sshx.Result{ExitCode: 0}, nil
	}
	err := h.svc.RunInstallJob(context.Background(), InstallPayload{
		HostID: h.host.ID, Strategy: "repo", AcceptSystemChanges: true,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "E_UNSUPPORTED_DISTRO") {
		t.Fatalf("expected the stage error, got %v", err)
	}
}

func TestParseComposePSHandlesBothFormats(t *testing.T) {
	v2 := `[{"Service":"teslamate","Name":"1mate-car-teslamate-1","State":"running"},{"Service":"database","Name":"1mate-car-database-1","State":"running"}]`
	states := parseComposePS(v2, 2)
	if len(states) != 2 || states["teslamate"] != "running" {
		t.Fatalf("v2 parse: %+v", states)
	}
	if got := aggregateStatus(states); got != model.InstRunning {
		t.Fatalf("aggregate=%s", got)
	}
	v1 := `{"Service":"teslamate","Name":"x","State":"exited"}
{"Service":"database","Name":"y","State":"exited"}`
	if got := aggregateStatus(parseComposePS(v1, 1)); got != model.InstStopped {
		t.Fatalf("v1 aggregate=%s", got)
	}
}

func TestPasswordNeverAppearsInCommands(t *testing.T) {
	h := newHarness(t)
	host, err := h.svc.CreateHost(HostInput{
		Name: "pw-host", Address: "10.0.0.11", SSHPort: 22, SSHUser: "deploy",
		AuthType: model.HostAuthPassword, Password: "s3cret-ssh-pass",
		SudoMode: model.SudoPassword, SudoPassword: "s3cret-sudo-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.host = host
	h.setProbeOutput(t, probeV2)
	if _, err := h.svc.ProbeHost(context.Background(), host, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.fake.Calls {
		if strings.Contains(c, "s3cret-sudo-pass") || strings.Contains(c, "s3cret-ssh-pass") {
			t.Fatalf("secret leaked into a command: %s", c)
		}
	}
	// The redactor must also mask it if it ever reaches a log line.
	if got := h.svc.Red.Apply("password=s3cret-sudo-pass"); strings.Contains(got, "s3cret") {
		t.Fatalf("redactor missed the secret: %s", got)
	}
	_ = time.Now()
	_ = ids.New()
}
