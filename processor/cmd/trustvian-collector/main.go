// Command trustvian-collector is a minimal, hand-assembled OpenTelemetry
// Collector binary that registers the trustvianprocessor alongside the
// standard OTLP receiver and the debug exporter. It exists so this
// module's example config.yaml (see ../../config.yaml) can actually run
// end-to-end, without depending on the external `ocb` (OpenTelemetry
// Collector Builder) tool — the Collector's own component APIs
// (go.opentelemetry.io/collector/otelcol) are enough to assemble a
// working binary directly.
//
// Run:
//
//	go run ./cmd/trustvian-collector --config=../../config.yaml
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"

	trustvianprocessor "trustvian-processor"
)

func components() (otelcol.Factories, error) {
	otlpFactory := otlpreceiver.NewFactory()
	debugFactory := debugexporter.NewFactory()
	trustvianFactory := trustvianprocessor.NewFactory()

	return otelcol.Factories{
		Receivers: map[component.Type]receiver.Factory{
			otlpFactory.Type(): otlpFactory,
		},
		Exporters: map[component.Type]exporter.Factory{
			debugFactory.Type(): debugFactory,
		},
		Processors: map[component.Type]processor.Factory{
			trustvianFactory.Type(): trustvianFactory,
		},
		Telemetry: newMinimalTelemetryFactory(),
	}, nil
}

// exitUsage matches trustvian-local and the platform command families: 2 is an
// invocation error.
const exitUsage = 2

const usage = `usage:
  trustvian-collector [--config <path>]
  trustvian-collector --version

Runs a minimal OpenTelemetry Collector with the Trustvian processor.

  --config   the Collector configuration file (default config.yaml);
             --config <path> and --config=<path> are equivalent
  --version  print the version and exit`

// version is the release version, written by the linker in a release build
// (`-ldflags -X main.version=vX.Y.Z`: scripts/release-build.sh and the
// Dockerfile) and never at run time. Go cannot stamp it from a tag: this
// module's path, trustvian-processor, is not under the repository's root
// module (ADR 0022). Empty in any other build, which reports Go's own build
// information instead.
var version string

// moduleVersion is the version the Collector reports as its build: the
// release version, or else Go's module version ("(devel)" from a checkout).
func moduleVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

// versionLine is what --version prints: the version, then the VCS revision
// Go recorded, as trustvian-local prints its own.
func versionLine() string {
	line := "trustvian-collector " + moduleVersion()
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				line += " " + setting.Value
			case "vcs.modified":
				if setting.Value == "true" {
					line += "+dirty"
				}
			}
		}
	}
	return line
}

// parseArgs returns the configuration path, and whether --version was asked
// for. Any argument it does not understand is an error, never ignored: falling
// back to ./config.yaml after an argument was given would run a configuration
// nobody named.
func parseArgs(args []string) (string, bool, error) {
	fs := flag.NewFlagSet("trustvian-collector", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "config.yaml", "the Collector configuration file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	if fs.NArg() != 0 {
		return "", false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *configPath == "" {
		return "", false, errors.New("--config needs a path")
	}
	return *configPath, *showVersion, nil
}

func main() {
	configPath, showVersion, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "trustvian-collector: %v\n%s\n", err, usage)
		os.Exit(exitUsage)
	}
	if showVersion {
		fmt.Println(versionLine())
		return
	}

	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "trustvian-collector",
			Description: "Minimal OTel Collector with the Trustvian processor",
			Version:     moduleVersion(),
		},
		Factories: components,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs: []string{"file:" + configPath},
				// fileprovider reads the config; envprovider resolves
				// ${env:VAR} references inside it. Both are the Collector's
				// own upstream providers — no Trustvian-side interpolation
				// engine exists or is needed.
				//
				// envprovider is what lets a deployment keep secrets out of
				// its config file: the reference deployment passes its
				// PostgreSQL DSN as an environment variable rather than
				// writing credentials into collector.yaml. See
				// deployments/docker-compose/.
				ProviderFactories: []confmap.ProviderFactory{
					fileprovider.NewFactory(),
					envprovider.NewFactory(),
				},
			},
		},
	}

	col, err := otelcol.NewCollector(settings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trustvian-collector: failed to construct: %v\n", err)
		os.Exit(1)
	}

	if err := col.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "trustvian-collector: run failed: %v\n", err)
		os.Exit(1)
	}
}
