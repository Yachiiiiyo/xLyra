package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"xlyra/server/internal/jsplugin"
)

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("package path is required"))
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal(err)
	}
	if err := verifyPackage(raw); err != nil {
		fatal(err)
	}
}

// verifyPackage applies the checks the server runs on upload: static
// validation, compile, and fixtures. On success it prints a summary.
func verifyPackage(raw []byte) error {
	pkg, report, analysis, err := inspectPackageWithAnalysis(raw)
	if err != nil {
		return err
	}
	signer := "unsigned"
	if pkg.Signer != "" {
		signer = "signed by " + pkg.Signer
	}
	fmt.Fprintf(os.Stdout, "ok  %s@%s (%s), %d fixtures, %s\n", pkg.Manifest.ID, pkg.Manifest.Version, pkg.Manifest.KindLabel(), len(pkg.Fixtures), signer)
	fmt.Fprintf(os.Stdout, "sha256 %s\n", pkg.SHA256)
	fmt.Fprintf(os.Stdout, "%s\n", formatAnalysisSummary(analysis))
	if report.Path != "" {
		fmt.Fprintf(os.Stdout, "icon %s %dx%d 不透明 %.0f%% 亮度 %.2f\n", report.Path, report.Width, report.Height, report.OpaqueRatio*100, report.Luminance)
	}
	if pkg.Manifest.DeclaresSiteType() || pkg.Manifest.Kind == "" {
		writeSiteProviderFixtures(pkg.Fixtures)
	}
	return nil
}

func formatAnalysisSummary(a jsplugin.ModuleAnalysis) string {
	globals := "none"
	if len(a.Globals) > 0 {
		globals = strings.Join(a.Globals, ",")
	}
	line := fmt.Sprintf("analysis functions=%d instructions=%d regexps=%d globals=%s", a.FunctionCount, a.InstructionCount, a.RegExpCount, globals)
	var flags []string
	if a.UsesEval {
		flags = append(flags, "eval")
	}
	if a.UsesFunctionCtor {
		flags = append(flags, "Function")
	}
	if a.UsesDynamicImport {
		flags = append(flags, "import()")
	}
	if a.UsesWith {
		flags = append(flags, "with")
	}
	if a.UsesAsync {
		flags = append(flags, "async")
	}
	if len(flags) > 0 {
		line += " flags=" + strings.Join(flags, ",")
	}
	return line
}

func inspectPackage(raw []byte) (jsplugin.Package, jsplugin.IconReport, error) {
	pkg, report, _, err := inspectPackageWithAnalysis(raw)
	return pkg, report, err
}

func inspectPackageWithAnalysis(raw []byte) (jsplugin.Package, jsplugin.IconReport, jsplugin.ModuleAnalysis, error) {
	pkg, plugin, err := loadPackage(raw)
	if err != nil {
		return jsplugin.Package{}, jsplugin.IconReport{}, jsplugin.ModuleAnalysis{}, err
	}
	report, err := jsplugin.CheckIconLegibility(raw, pkg.Manifest)
	if err != nil {
		return jsplugin.Package{}, jsplugin.IconReport{}, jsplugin.ModuleAnalysis{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugin.SelfTest(ctx); err != nil {
		return jsplugin.Package{}, jsplugin.IconReport{}, jsplugin.ModuleAnalysis{}, fmt.Errorf("selftest: %w", err)
	}
	return pkg, report, plugin.Analysis(), nil
}

// writeSiteProviderFixtures lists each sample with its name and hook. Other
// kinds keep the summary line only.
func writeSiteProviderFixtures(fixtures []jsplugin.Fixture) {
	if len(fixtures) == 0 {
		return
	}
	ordered := append([]jsplugin.Fixture(nil), fixtures...)
	sort.Slice(ordered, func(i, j int) bool {
		return fixtureSortKey(ordered[i]) < fixtureSortKey(ordered[j])
	})
	for _, fixture := range ordered {
		file := fixture.File
		if file == "" {
			file = fixture.Name
		}
		fmt.Fprintf(os.Stdout, "  %s (%s) hook=%s\n", file, fixture.Name, fixture.Hook)
	}
}

func fixtureSortKey(fixture jsplugin.Fixture) string {
	if fixture.File != "" {
		return fixture.File
	}
	return fixture.Name
}
