package provision

import (
	"strings"
	"testing"
)

func TestDefaultScriptPrefersV2AndIsIdempotent(t *testing.T) {
	script, err := Script(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1MATE_STAGE=", "1MATE_OK=1",
		"docker compose version",
		"docker-compose-plugin",
		"if docker info >/dev/null 2>&1 && docker compose version >/dev/null 2>&1",
		"already satisfied",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q", want)
		}
	}
	if strings.Contains(script, "pip install docker-compose") {
		t.Fatal("script must never install Compose v1")
	}
	if !strings.Contains(script, "never removes an existing Compose v1 binary") {
		t.Fatal("script should document that v1 is left alone")
	}
}

func TestScriptStrategyUsesConvenienceInstaller(t *testing.T) {
	o := DefaultOptions()
	o.Strategy = StrategyScript
	o.Mirror = "Aliyun"
	script, err := Script(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "https://get.docker.com") {
		t.Fatal("expected get.docker.com")
	}
	if !strings.Contains(script, "--dry-run") {
		t.Fatal("convenience installer must preview first")
	}
	if !strings.Contains(script, "MIRROR='Aliyun'") {
		t.Fatal("mirror not passed through")
	}
}

func TestComposeOnlySkipsEngine(t *testing.T) {
	o := DefaultOptions()
	o.Strategy = StrategyComposeOnly
	script, err := Script(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "MANAGE_ENGINE=0") {
		t.Fatal("compose-only must not manage the engine")
	}
	if !strings.Contains(script, "MANAGE_COMPOSE=1") {
		t.Fatal("compose-only still manages compose")
	}
	if !strings.Contains(script, "engine management disabled") {
		t.Fatal("compose-only must skip the engine install block")
	}
}

func TestRegistryMirrorsEmbeddedSafely(t *testing.T) {
	o := DefaultOptions()
	o.RegistryMirrors = []string{"https://mirror.gcr.io", "https://docker.mirrors.aliyun.com"}
	script, err := Script(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "https://mirror.gcr.io,https://docker.mirrors.aliyun.com") {
		t.Fatal("mirrors not embedded")
	}
}

func TestOptionsValidationRejectsInjection(t *testing.T) {
	cases := []Options{
		{Strategy: "shell", ComposeVersion: "2.29.7"},
		{Strategy: StrategyRepo, DockerVersion: "27.3.1; rm -rf /"},
		{Strategy: StrategyRepo, ComposeVersion: "$(id)"},
		{Strategy: StrategyRepo, Mirror: "evil`whoami`"},
		{Strategy: StrategyRepo, ComposeURLBase: "file:///etc/passwd"},
		{Strategy: StrategyRepo, RegistryMirrors: []string{"https://ok;rm -rf /"}},
	}
	for _, o := range cases {
		if err := o.Validate(); err == nil {
			t.Fatalf("expected rejection for %+v", o)
		}
	}
}

func TestMirrorFlags(t *testing.T) {
	o := DefaultOptions()
	o.Mirror = "AzureChinaCloud"
	script, _ := Script(o)
	if !strings.Contains(script, "https://mirror.azure.cn/docker-ce") {
		t.Fatal("azure mirror URL missing")
	}
}

func TestPinnedVersionUsed(t *testing.T) {
	o := DefaultOptions()
	o.ComposeVersion = "2.31.0"
	script, _ := Script(o)
	if !strings.Contains(script, "COMPOSE_VERSION='2.31.0'") {
		t.Fatal("pinned compose version missing")
	}
}
