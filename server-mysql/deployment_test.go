package main

import (
	"os"
	"strings"
	"testing"
)

// readRepoText 读取仓库内的文本文件并把 CRLF 归一化为 LF，
// 避免断言结果依赖检出时的行尾设置（.gitattributes / core.autocrlf）。
func readRepoText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func TestDeployScriptRollsBackWhenPublicProbeFails(t *testing.T) {
	script := readRepoText(t, "deploy/scripts/deploy-server.ps1")
	for _, required := range []string{
		"with socket.create_connection((host, game_port), timeout=10)",
		"Public TCP verification failed; restoring the previous binary.",
		"cp {backup} runtime/mhqserver",
		"docker compose up -d --no-deps --force-recreate game",
		"finally:\n    client.close()",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("deploy rollback is missing %q", required)
		}
	}
	probeIndex := strings.Index(script, "with socket.create_connection((host, game_port), timeout=10)")
	closeIndex := strings.Index(script, "finally:\n    client.close()")
	if probeIndex >= closeIndex {
		t.Fatal("public TCP check runs after the SSH client is closed")
	}
}

func TestDeployScriptPublishesValidatedGameplayConfig(t *testing.T) {
	script := readRepoText(t, "deploy/scripts/deploy-server.ps1")
	for _, required := range []string{
		"run .\\cmd\\publish-config -name Gameplay -source $GameplayConfigPath -validate-only",
		"MHQ_DEPLOY_GAMEPLAY_CONFIG_SHA256",
		"Uploaded %s SHA256 verified",
		"docker compose run --rm --no-deps",
		"--entrypoint /publish-config",
		"config reloaded revision=$revision names=[Gameplay]",
		"GAMEPLAY_PUBLICATION=OK revision=$revision",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("Gameplay deployment is missing %q", required)
		}
	}
	publicProbeIndex := strings.Index(script, "print(\"PUBLIC_TCP=OK\")")
	publicationIndex := strings.Index(script, "Publishing Gameplay configuration through the private Docker network")
	closeIndex := strings.Index(script, "finally:\n    client.close()")
	if publicProbeIndex >= publicationIndex {
		t.Fatal("Gameplay is published before the new public game server passes its TCP probe")
	}
	if publicationIndex >= closeIndex {
		t.Fatal("Gameplay is published after the pinned SSH connection is closed")
	}
}

func TestDeployScriptSynchronizesOperationYAMLAndEnablesWatcher(t *testing.T) {
	script := readRepoText(t, "deploy/scripts/deploy-server.ps1")
	for _, required := range []string{
		"MHQ_DEPLOY_OPERATIONS_DIR",
		"Uploaded operation YAML SHA256 verified",
		"config watcher published name=",
		"config watcher published CustomSkins",
		"config reloaded revision=$final_revision ",
		"OPERATION_YAML_HOT_RELOAD=OK",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("operation YAML deployment is missing %q", required)
		}
	}

	compose := readRepoText(t, "deploy/compose.yaml")
	if !strings.Contains(compose, "./config/operations:/config/operations:ro,Z") {
		t.Fatal("game container does not mount the operation YAML directory read-only")
	}
}

func TestReleaseBuildIncludesConfigPublisher(t *testing.T) {
	script := readRepoText(t, "deploy/scripts/build-linux-release.ps1")
	for _, required := range []string{
		"deploy\\tools",
		"publish-config",
		".\\cmd\\publish-config",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("release build is missing %q", required)
		}
	}
}

func TestDeploymentScriptsSupportNonRootSudoUser(t *testing.T) {
	for _, name := range []string{
		"deploy/scripts/deploy-server.ps1",
		"deploy/scripts/deploy-server-code-only.ps1",
		"deploy/scripts/deploy-gm.ps1",
	} {
		script := readRepoText(t, name)
		for _, required := range []string{
			`[string]$SshUser =`,
			`sudo -S -p '' -- bash -c`,
			`staging_dir = "/tmp/`,
		} {
			if !strings.Contains(script, required) {
				t.Fatalf("%s does not support non-root deployment: missing %q", name, required)
			}
		}
	}
}

func TestNormalizeAdvertiseAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"203.0.113.10:7756", "203.0.113.10:7756"},
		{" game.example.com:7756 ", "game.example.com:7756"},
		{"[2001:db8::10]:7756", "[2001:db8::10]:7756"},
	}
	for _, test := range tests {
		got, err := normalizeAdvertiseAddress(test.input)
		if err != nil {
			t.Fatalf("normalizeAdvertiseAddress(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("normalizeAdvertiseAddress(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeAdvertiseAddressRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "203.0.113.10", ":7756", "host:0", "host:65536", "http://host:7756"} {
		if got, err := normalizeAdvertiseAddress(input); err == nil {
			t.Errorf("normalizeAdvertiseAddress(%q) = %q, want error", input, got)
		}
	}
}
