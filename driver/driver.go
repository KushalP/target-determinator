// driver is a binary for driving a CI process based on the affected targets.
// Though the general flow of "determine targets" -> "run tests" -> "package binaries" could ideally
// be modelled as independent processes feeding into each other, in practice it can be useful to
// orchestrate these stages using a single high-context driver.
// For instance, the test phase should ideally be just `bazel test [targets]` but:
//  1. `bazel test [only-buildable-non-testable-targets] errors
//  2. `bazel test [no targets]` errors.
// Accordingly, being able to write logic in a programming language can be useful.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bazel-contrib/target-determinator/cli"
	"github.com/bazel-contrib/target-determinator/pkg"
	"github.com/bazel-contrib/target-determinator/third_party/protobuf/bazel/analysis"
	gazelle_label "github.com/bazelbuild/bazel-gazelle/label"
)

type driverFlags struct {
	commonFlags    *cli.CommonFlags
	revisionBefore string
	manualTestMode string
}

type config struct {
	Context        *pkg.Context
	RevisionBefore pkg.LabelledGitRev
	TargetPattern  gazelle_label.Pattern
	// One of "run" or "skip".
	ManualTestMode string
}

func main() {
	flags, err := parseFlags()
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Failed to parse flags: %v\n", err)
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s <before-revision>\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(flag.CommandLine.Output(), "Where <before-revision> may be any commit-like strings - full commit hashes, short commit hashes, tags, branches, etc.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Optional flags:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	config, err := resolveConfig(*flags)
	if err != nil {
		log.Fatalf("Error during preprocessing: %v", err)
	}

	var targets []gazelle_label.Label
	commandVerb := "build"

	log.Println("Discovering affected targets")
	callback := func(label gazelle_label.Label, differences []pkg.Difference, configuredTarget *analysis.ConfiguredTarget) {
		if config.ManualTestMode == "skip" && isTaggedManual(configuredTarget) {
			return
		}
		targets = append(targets, label)
		// This is not an ideal heuristic, ideally cquery would expose to us whether a target is a test target.
		if strings.HasSuffix(configuredTarget.GetTarget().GetRule().GetRuleClass(), "_test") {
			commandVerb = "test"
		}
	}

	if err := pkg.WalkAffectedTargets(config.Context,
		config.RevisionBefore,
		config.TargetPattern,
		false,
		callback); err != nil {
		log.Fatal(err)
	}

	if len(targets) == 0 {
		log.Println("No targets were affected, not running Bazel")
		os.Exit(0)
	}

	log.Printf("Discovered %d affected targets", len(targets))

	targetPatternFile, err := os.CreateTemp("", "")
	if err != nil {
		log.Fatalf("Failed to create temporary file for target patterns: %v", err)
	}
	for _, target := range targets {
		if _, err := targetPatternFile.WriteString(target.String()); err != nil {
			log.Fatalf("Failed to write target pattern to target pattern file: %v", err)
		}
		if _, err := targetPatternFile.WriteString("\n"); err != nil {
			log.Fatalf("Failed to write target pattern to target pattern file: %v", err)
		}
	}
	if err := targetPatternFile.Sync(); err != nil {
		log.Fatalf("Failed to sync target pattern file: %v", err)
	}
	if err := targetPatternFile.Close(); err != nil {
		log.Fatalf("Failed to close target pattern file: %v", err)
	}

	log.Printf("Running %s on %d targets", commandVerb, len(targets))
	cmd := exec.Command(config.Context.BazelPath, commandVerb, "--target_pattern_file", targetPatternFile.Name())
	cmd.Dir = config.Context.WorkspacePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}

func isTaggedManual(target *analysis.ConfiguredTarget) bool {
	for _, attr := range target.GetTarget().GetRule().GetAttribute() {
		if attr.GetName() == "tags" {
			for _, tag := range attr.GetStringListValue() {
				if tag == "manual" {
					return true
				}
			}
		}
	}
	return false
}

func parseFlags() (*driverFlags, error) {
	var flags driverFlags
	flags.commonFlags = cli.RegisterCommonFlags()
	flag.StringVar(&flags.manualTestMode, "manual-test-mode", "skip", "How to handle affected tests tagged manual. Possible values: run|skip")

	flag.Parse()

	if flags.manualTestMode != "run" && flags.manualTestMode != "skip" {
		return nil, fmt.Errorf("unexpected value for flag -manual-test-mode - allowed values: run|skip, saw: %s", flags.manualTestMode)
	}

	var err error
	flags.revisionBefore, err = cli.ValidateCommonFlags()
	if err != nil {
		return nil, err
	}

	return &flags, nil
}

func resolveConfig(flags driverFlags) (*config, error) {
	commonArgs, err := cli.ResolveCommonConfig(flags.commonFlags, flags.revisionBefore)
	if err != nil {
		return nil, err
	}

	return &config{
		Context:        commonArgs.Context,
		RevisionBefore: commonArgs.RevisionBefore,
		TargetPattern:  commonArgs.TargetPattern,
		ManualTestMode: flags.manualTestMode,
	}, nil
}
