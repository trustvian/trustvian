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

Runs a minimal OpenTelemetry Collector with the Trustvian processor.

  --config   the Collector configuration file (default config.yaml);
             --config <path> and --config=<path> are equivalent`

// parseArgs returns the configuration path. Any argument it does not
// understand is an error, never ignored: falling back to ./config.yaml after
// an argument was given would run a configuration nobody named.
func parseArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("trustvian-collector", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "config.yaml", "the Collector configuration file")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *configPath == "" {
		return "", errors.New("--config needs a path")
	}
	return *configPath, nil
}

func main() {
	configPath, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "trustvian-collector: %v\n%s\n", err, usage)
		os.Exit(exitUsage)
	}

	settings := otelcol.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "trustvian-collector",
			Description: "Minimal OTel Collector with the Trustvian processor",
			Version:     "0.0.0-dev",
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
