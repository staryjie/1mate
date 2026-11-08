package probe

import (
	"testing"

	"1mate/internal/model"
)

const sampleFullV2 = `IM_OS_ID=ubuntu
IM_OS_VERSION=24.04
IM_OS_CODENAME=noble
IM_OS_PRETTY=Ubuntu 24.04.1 LTS
IM_ARCH=x86_64
IM_KERNEL=6.8.0-45-generic
IM_CPUS=4
IM_MEM_MB=7900
IM_DISK_FREE_KB=48231000
IM_INIT=systemd
IM_CGROUP=2
IM_SUDO=nopasswd
IM_SUDO_NOPASS=1
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_CLIENT=27.3.1
IM_DOCKER_INFO=ok
IM_DOCKER_SERVER=27.3.1
IM_DOCKER_ROOTLESS=0
IM_DOCKER_SUDO=0
IM_DOCKER_ACTIVE=active
IM_COMPOSE_KIND=plugin_v2
IM_COMPOSE_VERSION=v2.29.7
IM_COMPOSE_PATH=/usr/libexec/docker/cli-plugins/docker-compose
IM_COMPOSE_V1_PRESENT=0
IM_LISTEN_PORTS=22,80,443
`

func TestParseFullV2(t *testing.T) {
	c, err := Parse("host-1", sampleFullV2)
	if err != nil {
		t.Fatal(err)
	}
	if c.OSID != "ubuntu" || c.OSCodename != "noble" || c.Arch != "x86_64" {
		t.Fatalf("unexpected os fields: %+v", c)
	}
	if !c.DockerPresent || c.SocketAccess != model.SocketOK || !c.DockerRunning {
		t.Fatalf("docker state wrong: %+v", c)
	}
	if c.DockerClient != "27.3.1" || c.DockerServer != "27.3.1" {
		t.Fatalf("versions: %+v", c)
	}
	if c.ComposeKind != model.ComposePluginV2 || c.ComposeMajor != 2 || c.ComposeVersion != "2.29.7" {
		t.Fatalf("compose: %+v", c)
	}
	if len(c.PortsInUse) != 3 {
		t.Fatalf("ports: %v", c.PortsInUse)
	}
	if st, _ := Status(c); st != model.HostOK {
		t.Fatalf("status=%s", st)
	}
}

func TestParseNoDocker(t *testing.T) {
	c, _ := Parse("h", `IM_OS_ID=debian
IM_OS_VERSION=12
IM_ARCH=x86_64
IM_DOCKER_PATH=
IM_DOCKER_INFO=absent
IM_COMPOSE_KIND=none
`)
	if c.DockerPresent {
		t.Fatal("docker should be absent")
	}
	if st, _ := Status(c); st != model.HostNoDocker {
		t.Fatalf("status=%s", st)
	}
}

func TestParseComposeV1Only(t *testing.T) {
	c, _ := Parse("h", `IM_OS_ID=debian
IM_OS_VERSION=11
IM_ARCH=aarch64
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_INFO=ok
IM_DOCKER_SERVER=20.10.24
IM_COMPOSE_KIND=standalone_v1
IM_COMPOSE_VERSION=1.29.2
IM_COMPOSE_V1_PRESENT=1
`)
	if c.ComposeKind != model.ComposeStandaloneV1 || c.ComposeMajor != 1 {
		t.Fatalf("compose: %+v", c)
	}
	if st, msg := Status(c); st != model.HostComposeV1 {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
}

func TestParseSocketDenied(t *testing.T) {
	c, _ := Parse("h", `IM_OS_ID=ubuntu
IM_OS_VERSION=22.04
IM_ARCH=x86_64
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_INFO=denied
IM_DOCKER_SERVER=24.0.7
IM_COMPOSE_KIND=plugin_v2
IM_COMPOSE_VERSION=2.24.0
`)
	if c.SocketAccess != model.SocketDenied {
		t.Fatalf("socket=%s", c.SocketAccess)
	}
	if st, _ := Status(c); st != model.HostDockerDenied {
		t.Fatalf("status=%s", st)
	}
}

func TestParseArmv7Unsupported(t *testing.T) {
	c, _ := Parse("h", `IM_OS_ID=raspbian
IM_OS_VERSION=11
IM_ARCH=armv7l
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_INFO=ok
IM_COMPOSE_KIND=plugin_v2
IM_COMPOSE_VERSION=2.24.0
`)
	if c.UnsupportedReason == "" {
		t.Fatal("expected unsupported reason")
	}
	if st, _ := Status(c); st != model.HostUnsupported {
		t.Fatalf("status=%s", st)
	}
}

func TestParseRootless(t *testing.T) {
	c, _ := Parse("h", `IM_OS_ID=fedora
IM_OS_VERSION=41
IM_ARCH=aarch64
IM_DOCKER_PATH=/usr/bin/docker
IM_DOCKER_INFO=ok
IM_DOCKER_ROOTLESS=1
IM_COMPOSE_KIND=plugin_v2
IM_COMPOSE_VERSION=2.30.0
`)
	if !c.DockerRootless {
		t.Fatal("expected rootless")
	}
	if st, msg := Status(c); st != model.HostOK || msg == "" {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
}

func TestParseMissingOS(t *testing.T) {
	c, _ := Parse("h", `IM_ARCH=x86_64`)
	if c.UnsupportedReason == "" {
		t.Fatal("expected unsupported reason without an OS")
	}
}
