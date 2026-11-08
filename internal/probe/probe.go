package probe

import (
	"bufio"
	"strconv"
	"strings"

	"1mate/internal/model"
)

// Script is a read-only capability collector. Every value is emitted as a
// single IM_* line so the parser stays independent of platform quirks.
const Script = `#!/bin/sh
# 1mate capability probe (read-only)
emit() { printf 'IM_%s=%s\n' "$1" "$2"; }

if [ -r /etc/os-release ]; then
  . /etc/os-release
  OS_ID="$ID"; OS_VER="$VERSION_ID"; OS_CODE="$VERSION_CODENAME"; OS_NAME="$PRETTY_NAME"
else
  OS_ID=""; OS_VER=""; OS_CODE=""; OS_NAME="$(uname -s)"
fi
emit OS_ID "$OS_ID"
emit OS_VERSION "$OS_VER"
emit OS_CODENAME "$OS_CODE"
emit OS_PRETTY "$OS_NAME"
emit ARCH "$(uname -m)"
emit KERNEL "$(uname -r)"
emit CPUS "$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 0)"

MEM_MB=$(awk '/^MemTotal:/ {printf "%d", $2/1024}' /proc/meminfo 2>/dev/null || echo 0)
emit MEM_MB "$MEM_MB"

DISK_KB=$(df -Pk /opt 2>/dev/null | awk 'NR==2 {print $4}')
[ -z "$DISK_KB" ] && DISK_KB=$(df -Pk / 2>/dev/null | awk 'NR==2 {print $4}')
[ -z "$DISK_KB" ] && DISK_KB=0
emit DISK_FREE_KB "$DISK_KB"

if command -v systemctl >/dev/null 2>&1; then
  if [ "$(readlink /proc/1/comm 2>/dev/null)" = "systemd" ]; then
    emit INIT systemd
  else
    emit INIT systemctl
  fi
else
  emit INIT other
fi
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  emit CGROUP 2
else
  emit CGROUP 1
fi

# Privilege state
if [ "$(id -u)" = "0" ]; then
  emit SUDO root
  emit SUDO_NOPASS 1
elif command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    emit SUDO nopasswd
    emit SUDO_NOPASS 1
  else
    emit SUDO password
    emit SUDO_NOPASS 0
  fi
else
  emit SUDO none
  emit SUDO_NOPASS 0
fi

# Docker engine
DOCKER_PATH=$(command -v docker 2>/dev/null)
emit DOCKER_PATH "$DOCKER_PATH"
if [ -n "$DOCKER_PATH" ]; then
  emit DOCKER_CLIENT "$($DOCKER_PATH --version 2>/dev/null | sed -n 's/^Docker version \([^,]*\),.*/\1/p' | head -1)"
  DINFO=$($DOCKER_PATH info --format '{{.ServerVersion}}' 2>&1)
  if [ $? -eq 0 ]; then
    emit DOCKER_INFO ok
    emit DOCKER_SERVER "$DINFO"
    emit DOCKER_SUDO 0
  else
    case "$DINFO" in
      *"permission denied"*|*"Permission denied"*|*"Got permission denied"*)
        emit DOCKER_INFO denied
        ;;
      *)
        emit DOCKER_INFO error
        ;;
    esac
    emit DOCKER_SERVER ""
    # Retry through passwordless sudo only; never prompt during a probe.
    if sudo -n true >/dev/null 2>&1; then
      SRV=$(sudo -n "$DOCKER_PATH" info --format '{{.ServerVersion}}' 2>/dev/null)
      if [ -n "$SRV" ]; then
        emit DOCKER_INFO ok
        emit DOCKER_SERVER "$SRV"
        emit DOCKER_SUDO 1
      fi
    fi
  fi
  emit DOCKER_ROOTLESS "$($DOCKER_PATH info --format '{{.SecurityOptions}}' 2>/dev/null | grep -c rootless)"
else
  emit DOCKER_CLIENT ""
  emit DOCKER_INFO absent
  emit DOCKER_SERVER ""
  emit DOCKER_ROOTLESS 0
  emit DOCKER_SUDO 0
fi

if command -v systemctl >/dev/null 2>&1; then
  emit DOCKER_ACTIVE "$(systemctl is-active docker 2>/dev/null)"
else
  emit DOCKER_ACTIVE unknown
fi

# Docker Compose: plugin v2 first, then standalone binaries.
CKIND=none
CVER=""
CPATH=""
if [ -n "$DOCKER_PATH" ]; then
  if $DOCKER_PATH compose version >/dev/null 2>&1; then
    CKIND=plugin_v2
    CVER=$($DOCKER_PATH compose version --short 2>/dev/null)
    if [ -z "$CVER" ]; then
      CVER=$($DOCKER_PATH compose version 2>/dev/null | sed -n 's/.*version \(v[0-9][^ ,]*\).*/\1/p' | head -1)
    fi
    for P in /usr/libexec/docker/cli-plugins/docker-compose /usr/lib/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose "$HOME/.docker/cli-plugins/docker-compose"; do
      if [ -x "$P" ]; then CPATH="$P"; break; fi
    done
  fi
fi
if [ "$CKIND" = none ] && command -v docker-compose >/dev/null 2>&1; then
  CV=$(docker-compose version --short 2>/dev/null)
  if [ -z "$CV" ]; then
    CV=$(docker-compose version 2>/dev/null | sed -n 's/.*version \(v\?[0-9][^ ,]*\).*/\1/p' | head -1)
  fi
  case "$CV" in
    v2*|2.*) CKIND=standalone_v2 ;;
    v1*|1.*) CKIND=standalone_v1 ;;
  esac
  CVER="$CV"
  CPATH=$(command -v docker-compose 2>/dev/null)
fi
emit COMPOSE_KIND "$CKIND"
emit COMPOSE_VERSION "$CVER"
emit COMPOSE_PATH "$CPATH"
if command -v docker-compose >/dev/null 2>&1; then
  emit COMPOSE_V1_PRESENT 1
else
  emit COMPOSE_V1_PRESENT 0
fi

# Ports already listening on the host.
if command -v ss >/dev/null 2>&1; then
  PORTS=$(ss -ltnH 2>/dev/null | awk '{print $4}' | sed 's/.*://' | sort -n | uniq | paste -sd, -)
elif command -v netstat >/dev/null 2>&1; then
  PORTS=$(netstat -ltn 2>/dev/null | awk 'NR>2 {print $4}' | sed 's/.*://' | sort -n | uniq | paste -sd, -)
else
  PORTS=""
fi
emit LISTEN_PORTS "$PORTS"
emit PROBE_DONE 1
`

// Parse converts the script output into capabilities. Unknown keys are ignored
// so newer probes stay compatible with older parsers.
func Parse(hostID, output string) (*model.Capabilities, error) {
	values := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "IM_") {
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(line, "IM_"), "=", 2)
		if len(kv) != 2 {
			continue
		}
		values[kv[0]] = kv[1]
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	c := &model.Capabilities{
		HostID:         hostID,
		OSID:           strings.ToLower(values["OS_ID"]),
		OSVersion:      values["OS_VERSION"],
		OSCodename:     values["OS_CODENAME"],
		OSPretty:       values["OS_PRETTY"],
		Arch:           values["ARCH"],
		Kernel:         values["KERNEL"],
		InitSystem:     values["INIT"],
		Cgroup:         values["CGROUP"],
		CPUCount:       atoi(values["CPUS"]),
		MemMB:          atoi(values["MEM_MB"]),
		DiskFreeKB:     atoi64(values["DISK_FREE_KB"]),
		DockerClient:   strings.TrimPrefix(values["DOCKER_CLIENT"], "v"),
		DockerServer:   strings.TrimPrefix(values["DOCKER_SERVER"], "v"),
		DockerSudo:     values["DOCKER_SUDO"] == "1",
		DockerRootless: values["DOCKER_ROOTLESS"] == "1",
		ComposeVersion: strings.TrimPrefix(values["COMPOSE_VERSION"], "v"),
		ComposePath:    values["COMPOSE_PATH"],
		ComposeV1Present: values["COMPOSE_V1_PRESENT"] == "1",
		PortsInUse:     parsePorts(values["LISTEN_PORTS"]),
	}

	c.DockerPresent = values["DOCKER_PATH"] != ""
	switch values["DOCKER_INFO"] {
	case "ok":
		c.SocketAccess = model.SocketOK
	case "denied":
		c.SocketAccess = model.SocketDenied
	default:
		c.SocketAccess = model.SocketAbsent
	}
	c.DockerRunning = c.SocketAccess == model.SocketOK

	switch values["COMPOSE_KIND"] {
	case "plugin_v2":
		c.ComposeKind = model.ComposePluginV2
	case "standalone_v2":
		c.ComposeKind = model.ComposeStandaloneV2
	case "standalone_v1":
		c.ComposeKind = model.ComposeStandaloneV1
	default:
		c.ComposeKind = model.ComposeNone
	}
	c.ComposeMajor = c.ComposeKind.Major()
	c.UnsupportedReason = unsupportedReason(c)
	return c, nil
}

func unsupportedReason(c *model.Capabilities) string {
	switch {
	case strings.HasPrefix(c.Arch, "armv7") || c.Arch == "armv6l" || c.Arch == "i386" || c.Arch == "i686":
		return "TeslaMate requires a 64-bit host; " + c.Arch + " is not supported"
	case c.OSID == "" :
		return "cannot determine the operating system from /etc/os-release"
	}
	return ""
}

// Status maps capabilities to the host status shown in the UI.
func Status(c *model.Capabilities) (model.HostStatus, string) {
	if c.UnsupportedReason != "" {
		return model.HostUnsupported, c.UnsupportedReason
	}
	if !c.DockerPresent {
		return model.HostNoDocker, "Docker is not installed"
	}
	if c.SocketAccess == model.SocketDenied {
		return model.HostDockerDenied, "the SSH user cannot access the Docker socket (permission denied)"
	}
	if c.SocketAccess == model.SocketAbsent {
		return model.HostNoDocker, "Docker is installed but the daemon is not reachable"
	}
	switch c.ComposeKind {
	case model.ComposeNone:
		return model.HostNoCompose, "Docker Compose is not installed"
	case model.ComposeStandaloneV1:
		return model.HostComposeV1, "only Docker Compose v1 (" + c.ComposeVersion + ") is installed; v1 is end-of-life"
	}
	if c.DockerRootless {
		return model.HostOK, "rootless Docker detected"
	}
	return model.HostOK, ""
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func parsePorts(s string) []int {
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n > 0 && n <= 65535 {
			out = append(out, n)
		}
	}
	return out
}
