//go:build alloycustombuild

// Package custombuild builds custom Alloy binaries the way `make alloy` does,
// with different selections of native components, checks that each binary
// contains what was selected, and logs the size of each binary.
//
// The builds are slow, so the tests only run with the alloycustombuild build
// tag. Run them with `make test-custom-builds`.
package custombuild

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario is one custom build of Alloy.
type scenario struct {
	name string
	// alloyComponents is the content of alloy-components.yaml.
	alloyComponents string
	// converters is whether alloyComponents enables the config converter.
	converters bool
	// included and excluded are components which must and must not be in the
	// binary. Each one needs a config in componentConfigs.
	included, excluded []string
}

var scenarios = []scenario{
	{
		name:            "default",
		alloyComponents: "components: [\"*\"]\nconverters: true\n",
		converters:      true,
		included:        []string{"loki.write", "prometheus.scrape", "otelcol.receiver.otlp"},
	},
	{
		name:            "loki-file",
		alloyComponents: "components: [local.file_match, loki.source.file, loki.process, loki.write]\nconverters: false\n",
		included:        []string{"loki.write", "local.file_match"},
		excluded:        []string{"prometheus.scrape", "otelcol.receiver.otlp"},
	},
	{
		name:            "prometheus",
		alloyComponents: "components: [prometheus.*]\nconverters: false\n",
		included:        []string{"prometheus.scrape"},
		excluded:        []string{"loki.write"},
	},
	{
		name:            "beyla",
		alloyComponents: "components: [beyla.ebpf]\nconverters: false\n",
		excluded:        []string{"loki.write"},
	},
}

// componentConfigs are valid configs which use a single component.
var componentConfigs = map[string]string{
	"local.file_match": `
local.file_match "test" {
  path_targets = []
}`,
	"loki.write": `
loki.write "test" {
  endpoint {
    url = "http://localhost:3100/loki/api/v1/push"
  }
}`,
	"prometheus.scrape": `
prometheus.scrape "test" {
  targets    = []
  forward_to = []
}`,
	"otelcol.receiver.otlp": `
otelcol.receiver.otlp "test" {
  grpc {}
  output {}
}`,
}

// prometheusConfig is converted to check whether the config converter is
// included.
const prometheusConfig = `
scrape_configs:
  - job_name: test
    static_configs:
      - targets: ["localhost:9090"]
`

func TestCustomBuilds(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			bin := build(t, repoRoot, sc)
			checkBinary(t, bin, sc)

			info, err := os.Stat(bin)
			require.NoError(t, err)
			t.Logf("binary size on %s/%s: %.1f MB", runtime.GOOS, runtime.GOARCH, float64(info.Size())/1e6)
		})
	}
}

// build copies the collector module, applies the scenario, and builds Alloy
// the way `make alloy` does. It returns the path of the binary.
func build(t *testing.T, repoRoot string, sc scenario) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "collector")
	copyCollectorModule(t, filepath.Join(repoRoot, "collector"), dir, repoRoot)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alloy-components.yaml"), []byte(sc.alloyComponents), 0o644))

	// Same as `make generate-otel-collector-distro`, which runs the steps in
	// generate.go. Generation runs on the host, without cgo.
	run(t, dir, hostEnv(), "go", "generate")

	bin := filepath.Join(dir, "alloy")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// Same flags as `RELEASE_BUILD=1 make alloy`, which strips the binary.
	run(t, dir, os.Environ(), "go", "build", "-tags", os.Getenv("GO_TAGS"), "-ldflags", "-s -w", "-o", bin, ".")
	return bin
}

// copyCollectorModule copies the collector module from src to dst. Relative
// replace directives are rewritten to point at repoRoot, so the copy builds
// against the Alloy sources it was copied from.
func copyCollectorModule(t *testing.T, src, dst, repoRoot string) {
	t.Helper()

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Don't copy this package into the copy.
			if rel == "custombuild" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rel == "go.mod" || rel == "builder-config.yaml" {
			data = rewriteRelativeReplaces(data, repoRoot)
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	require.NoError(t, err)
}

var relativeReplace = regexp.MustCompile(`=>\s*\.\./`)

func rewriteRelativeReplaces(data []byte, repoRoot string) []byte {
	return relativeReplace.ReplaceAll(data, []byte("=> "+filepath.ToSlash(repoRoot)+"/"))
}

// checkBinary checks that the binary contains the components and features
// selected in the scenario.
func checkBinary(t *testing.T, bin string, sc scenario) {
	t.Helper()
	dir := t.TempDir()

	for _, name := range sc.included {
		out, err := validate(t, bin, dir, name)
		require.NoError(t, err, "%s should be included, but validation failed:\n%s", name, out)
	}
	for _, name := range sc.excluded {
		out, err := validate(t, bin, dir, name)
		require.Error(t, err, "%s should not be included, but validation passed", name)
		require.Contains(t, out, fmt.Sprintf("cannot find the definition of component name %q", name))
	}

	in := filepath.Join(dir, "prometheus.yaml")
	require.NoError(t, os.WriteFile(in, []byte(prometheusConfig), 0o644))
	out, err := exec.Command(bin, "convert", "--source-format=prometheus", "--output="+filepath.Join(dir, "converted.alloy"), in).CombinedOutput()
	if sc.converters {
		require.NoError(t, err, "alloy convert should work:\n%s", out)
	} else {
		require.Error(t, err, "alloy convert should fail without the converters")
	}

	out, err = exec.Command(bin, "otel", "components").CombinedOutput()
	require.NoError(t, err, "alloy otel components should work:\n%s", out)
}

// validate runs `alloy validate` against a config which uses the component.
func validate(t *testing.T, bin, dir, component string) (string, error) {
	t.Helper()

	cfg, ok := componentConfigs[component]
	require.True(t, ok, "no config for %s in componentConfigs", component)

	path := filepath.Join(dir, strings.ReplaceAll(component, ".", "_")+".alloy")
	require.NoError(t, os.WriteFile(path, []byte(cfg+"\n"), 0o644))
	out, err := exec.Command(bin, "validate", path).CombinedOutput()
	return string(out), err
}

func run(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %s failed:\n%s", name, strings.Join(args, " "), out)
}

// hostEnv is the environment for code generation: the host platform and no
// cgo, like GO_HOST_ENV in the Makefile.
func hostEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOOS=") || strings.HasPrefix(kv, "GOARCH=") || strings.HasPrefix(kv, "CGO_ENABLED=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "CGO_ENABLED=0")
}
