package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	//go:embed main_alloy.tpl
	templateMainAlloy []byte
	//go:embed main_windows.tpl
	templateMainWindows []byte
)

const fileHeader = "// GENERATED CODE: DO NOT EDIT\n\n"

func main() {
	log.Println("Generating Alloy OTel Collector main file...")
	var path, alloyComponentsPath string
	flag.StringVar(&path, "path", "", "path to put generated files")
	flag.StringVar(&alloyComponentsPath, "alloy-components", "", "path to the file which selects the native Alloy components; every component is included if empty")
	flag.Parse()

	log.Printf("path: %v", path)

	if err := generate(path, alloyComponentsPath); err != nil {
		log.Fatal(err)
	}
}

// generate post-processes the OCB output in path according to the native
// component selection at alloyComponentsPath.
func generate(path, alloyComponentsPath string) error {
	cfg, err := readAlloyConfig(alloyComponentsPath)
	if err != nil {
		return fmt.Errorf("failed to read alloy components config: %w", err)
	}

	if err := copyAlloyMainTemplateFromFile(path); err != nil {
		return fmt.Errorf("failed to copy alloy main template: %w", err)
	}

	if err := replaceSectionsOfGeneratedMainFile(path); err != nil {
		return fmt.Errorf("failed to replace command factory: %w", err)
	}

	if err := replaceMainWindows(path); err != nil {
		return fmt.Errorf("failed to replace main_windows.go: %w", err)
	}

	if err := writeAlloyComponents(path, cfg); err != nil {
		return fmt.Errorf("failed to generate components_alloy.go: %w", err)
	}
	return nil
}

func copyAlloyMainTemplateFromFile(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create dst dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(path, "main_alloy.go"), append([]byte(fileHeader), templateMainAlloy...), 0o644); err != nil {
		return fmt.Errorf("write template to %s: %w", path, err)
	}
	return nil
}

func replaceSectionsOfGeneratedMainFile(path string) error {
	main := filepath.Join(path, "main.go")

	data, err := os.ReadFile(main)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	lines, err = replaceCmdFactory(lines)

	if err != nil {
		return fmt.Errorf("error replacing command factory in %s: %w", path, err)
	}

	lines, err = addReleasePleaseVersioning(lines)

	if err != nil {
		return fmt.Errorf("error setting collector version in %s: %w", path, err)
	}

	newContent := strings.Join(lines, "\n")
	fi, err := os.Stat(main)
	var mode os.FileMode = 0o644
	if err == nil {
		mode = fi.Mode()
	}

	if err := os.WriteFile(main, []byte(newContent), mode); err != nil {
		return fmt.Errorf("error writing file: %w", err)
	}

	return nil
}

// replaceCmdFactory processes incoming lines and will search for
// "cmd := otelcol.NewCommand(params)" and replace it with
// "cmd := newAlloyCommand(params)"
func replaceCmdFactory(lines []string) ([]string, error) {
	const target = "cmd := otelcol.NewCommand(params)"
	const replacement = "cmd := newAlloyCommand(params)"

	replaced := false
	for i, line := range lines {
		if strings.Contains(line, target) {
			lines[i] = strings.Replace(line, target, replacement, 1)
			replaced = true
			break
		}
	}

	if !replaced {
		return nil, fmt.Errorf("target line not found")
	}

	return lines, nil
}

// replaceCmdFactory processes incoming lines and will search for
// `Version: "..."` and replace it with
// `Version: CollectorVersion()`
func addReleasePleaseVersioning(lines []string) ([]string, error) {
	versionPattern := regexp.MustCompile(`^(\s+Version:\s+)"[^"]+"(,)(\s*//.*)?$`)
	versionReplaced := false
	for i, line := range lines {
		if matches := versionPattern.FindStringSubmatch(line); matches != nil {
			lines[i] = matches[1] + `CollectorVersion()` + matches[2]
			versionReplaced = true
			break
		}
	}

	if !versionReplaced {
		return nil, fmt.Errorf("version field not found")
	}

	return lines, nil
}

func replaceMainWindows(path string) error {
	if err := os.WriteFile(filepath.Join(path, "main_windows.go"), append([]byte(fileHeader), templateMainWindows...), 0o644); err != nil {
		return fmt.Errorf("error writing file: %w", err)
	}
	return nil
}

const (
	allComponentsPackage = "github.com/grafana/alloy/internal/component/all"
	converterPackage     = "github.com/grafana/alloy/internal/converter/install"
)

// alloyConfig is the content of alloy-components.yaml, which selects the
// native Alloy components. It is separate from the OCB builder config because
// OCB doesn't know about native components.
type alloyConfig struct {
	// Components are patterns of native Alloy component names to include in
	// the build, such as "loki.source.file" or "prometheus.*". "*" includes
	// every component, and so does leaving Components unset.
	Components *[]string `yaml:"components"`
	// Converters controls whether `alloy convert` and running Alloy with a
	// non-Alloy --config.format are supported. Defaults to true.
	Converters *bool `yaml:"converters"`
}

// writeAlloyComponents generates components_alloy.go, which imports the
// native Alloy components (and optionally the config converter) selected in
// alloy-components.yaml.
func writeAlloyComponents(path string, cfg alloyConfig) error {
	imports, err := componentImports(cfg.Components)
	if err != nil {
		return err
	}
	if cfg.Converters == nil || *cfg.Converters {
		imports = append(imports, converterPackage)
	}

	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	buf.WriteString("package main\n")
	if len(imports) > 0 {
		buf.WriteString("\n// Native Alloy components and features selected in alloy-components.yaml.\n")
		buf.WriteString("import (\n")
		for _, imp := range imports {
			fmt.Fprintf(&buf, "\t_ %q\n", imp)
		}
		buf.WriteString(")\n")
	}

	return os.WriteFile(filepath.Join(path, "components_alloy.go"), buf.Bytes(), 0o644)
}

// readAlloyConfig reads alloy-components.yaml. If path is empty, or the file
// is empty, the defaults apply: every component and the converters.
func readAlloyConfig(path string) (alloyConfig, error) {
	var cfg alloyConfig
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	// Fail on unknown keys, so a typo doesn't silently select every component.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// componentImports returns the sorted list of packages to import for the
// selected component patterns.
func componentImports(patterns *[]string) ([]string, error) {
	// Import every component through the all package, so that new components
	// are included without regenerating anything.
	if patterns == nil || slices.Contains(*patterns, "*") {
		return []string{allComponentsPackage}, nil
	}
	return resolveComponentPackages(*patterns)
}

// resolveComponentPackages maps component name patterns to the packages which
// register the matching components. Several components may be registered by
// the same package.
func resolveComponentPackages(patterns []string) ([]string, error) {
	// There is nothing to resolve, so don't compile every component.
	if len(patterns) == 0 {
		return nil, nil
	}

	known, err := lookupComponentPackages()
	if err != nil {
		return nil, err
	}

	var (
		pkgs    []string
		unknown []string
	)
	for _, pattern := range patterns {
		matched := false
		for name, pkg := range known {
			ok, err := path.Match(pattern, name)
			if err != nil {
				return nil, fmt.Errorf("invalid component pattern %q in alloy-components.yaml: %w", pattern, err)
			}
			if ok {
				matched = true
				pkgs = append(pkgs, pkg)
			}
		}
		if !matched {
			unknown = append(unknown, unknownComponentMessage(pattern, known))
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown components in alloy-components.yaml:\n  %s", strings.Join(unknown, "\n  "))
	}

	slices.Sort(pkgs)
	return slices.Compact(pkgs), nil
}

// lookupComponentPackages returns a map of component names to the packages
// which register them. It is a variable so tests can replace it.
var lookupComponentPackages = knownComponentPackages

// knownComponentPackages runs the componentpkgs helper, which compiles every
// native Alloy component and reports which package registers each of them.
func knownComponentPackages() (map[string]string, error) {
	log.Println("Resolving native Alloy component packages...")

	cmd := exec.Command("go", "run", "github.com/grafana/alloy/otel_engine/generator/componentpkgs")
	cmd.Stderr = os.Stderr
	// Generation runs with CGO_ENABLED=0, but some components only build with
	// cgo on some platforms. Fall back to Go's default, like the Alloy build.
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CGO_ENABLED=")
	})
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run componentpkgs: %w", err)
	}

	var known map[string]string
	if err := json.Unmarshal(out, &known); err != nil {
		return nil, fmt.Errorf("parse componentpkgs output: %w", err)
	}
	return known, nil
}

func unknownComponentMessage(name string, known map[string]string) string {
	namespace, _, _ := strings.Cut(name, ".")

	var similar []string
	for k := range known {
		if strings.HasPrefix(k, namespace+".") {
			similar = append(similar, k)
		}
	}
	if len(similar) == 0 {
		return fmt.Sprintf("%q", name)
	}
	slices.Sort(similar)
	return fmt.Sprintf("%q (components in the %q namespace: %s)", name, namespace, strings.Join(similar, ", "))
}
