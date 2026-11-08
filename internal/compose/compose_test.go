package compose

import (
	"strings"
	"testing"

	"1mate/internal/model"
)

func spec() Spec {
	return Spec{
		Slug:             "home-car",
		ProjectName:      ProjectName("home-car"),
		BindAddress:      "127.0.0.1",
		TMPort:           4000,
		GrafanaPort:      3000,
		MQTTPort:         1883,
		Timezone:         "Asia/Shanghai",
		Region:           "cn",
		ImageTag:         "latest",
		PostgresTag:      "18-trixie",
		GrafanaAdminUser: "admin",
		EncryptionKey:    "0123456789abcdef0123456789abcdef",
		DBUser:           "teslamate",
		DBPass:           "db-password-value",
		GrafanaPassword:  "grafana-password",
	}
}

func TestRenderV2(t *testing.T) {
	out, err := Render(spec(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: 1mate-home-car") {
		t.Fatal("v2 file must declare the project name")
	}
	if strings.Contains(out, "version:") {
		t.Fatal("v2 file must not declare a version key")
	}
	for _, want := range []string{"teslamate/teslamate:${TM_IMAGE_TAG}", "postgres:${TM_PG_TAG}",
		"teslamate/grafana:${TM_IMAGE_TAG}", "eclipse-mosquitto:2", "teslamate-db:/var/lib/postgresql"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "db-password-value") {
		t.Fatal("compose file must not contain secrets")
	}
}

func TestRenderV1Legacy(t *testing.T) {
	out, err := Render(spec(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `version: "3.9"`) {
		t.Fatal("v1 file must declare a version")
	}
	if strings.Contains(out, "name: 1mate-") {
		t.Fatal("v1 file must not declare a project name")
	}
}

func TestRenderEnvHasSecretsAndRegion(t *testing.T) {
	env, err := RenderEnv(spec())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TM_ENCRYPTION_KEY=0123456789abcdef0123456789abcdef",
		"TM_DB_PASS=db-password-value", "TM_REGION=cn",
		"TM_API_HOST=https://owner-api.vn.cloud.tesla.cn", "TM_DISABLE_MQTT=false"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q in env:\n%s", want, env)
		}
	}
}

func TestRenderEnvRejectsManagedKey(t *testing.T) {
	s := spec()
	s.ExtraEnv = map[string]string{"TM_DB_PASS": "override"}
	if _, err := RenderEnv(s); err == nil {
		t.Fatal("expected error overriding a managed variable")
	}
}

func TestValidateSlug(t *testing.T) {
	for _, ok := range []string{"ab", "home-car", "car1"} {
		if err := ValidateSlug(ok); err != nil {
			t.Fatalf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "Home-Car", "-car", "car-", "car_car", strings.Repeat("a", 32)} {
		if err := ValidateSlug(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestCommandPerComposeKind(t *testing.T) {
	cases := map[model.ComposeKind]string{
		model.ComposePluginV2:     "docker compose",
		model.ComposeStandaloneV2: "docker-compose",
		model.ComposeStandaloneV1: "docker-compose",
	}
	for kind, want := range cases {
		got, ok := Command(kind)
		if !ok || got != want {
			t.Fatalf("%s: got %q", kind, got)
		}
	}
	if _, ok := Command(model.ComposeNone); ok {
		t.Fatal("no command should exist without compose")
	}
}

func TestChecksumStable(t *testing.T) {
	a, _ := Render(spec(), 2)
	b, _ := Render(spec(), 2)
	if Checksum(a) != Checksum(b) {
		t.Fatal("checksum must be stable for identical input")
	}
}
