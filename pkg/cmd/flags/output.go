package flags

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aquasecurity/tracee/pkg/config"
	"github.com/aquasecurity/tracee/pkg/errfmt"
)

func outputHelp() string {
	return `Control how and where output is printed.
Format options:
table[:/path/to/file,...]                          output events in table format (default). The default path to file is stdout.
table-verbose[:/path/to/file,...]                  output events in table format with extra fields per event. The default path to file is stdout.
json[:/path/to/file,...]                           output events in json format. The default path to file is stdout.
gob[:/path/to/file,...]                            output events in gob format. The default path to file is stdout.
gotemplate=/path/to/template[:/path/to/file,...]   output events formatted using a given gotemplate file. The default path to file is stdout.
none                                               ignore stream of events output, usually used with --capture

Fluent Forward options:
forward:url                                        send events in json format using the Forward protocol to a Fluent receiver

Webhook options:
webhook:url                                        send events in json format to the webhook url

Other options:
option:{stack-addresses,exec-env,relative-time,exec-hash,parse-arguments,sort-events}
                                                   augment output according to given options (default: none)
  stack-addresses                                  include stack memory addresses for each event
  exec-env                                         when tracing execve/execveat, show the environment variables that were used for execution
  relative-time                                    use relative timestamp instead of wall timestamp for events
  exec-hash                                        when tracing sched_process_exec, show the file hash(sha256) and ctime
  parse-arguments                                  do not show raw machine-readable values for event arguments, instead parse into human readable strings
  parse-arguments-fds                              enable parse-arguments and enrich fd with its file path translation. This can cause pipeline slowdowns.
  sort-events                                      enable sorting events before passing to them output. This will decrease the overall program efficiency.

Examples:
  --output json                                                  | output as json to stdout
  --output json:/my/out                                          | output as json to /my/out
  --output gotemplate=/path/to/my.tmpl                           | output as the provided go template to stdout
  --output gob:/my/out                                           | output gob to /my/out
  --output json --output gob:/my/out                             | output as json to stdout and as gob to /my/out
  --output json:/my/out1,/my/out2                                | output as json to both /my/out and /my/out2
  --output none                                                  | ignore events output
  --output table --output option:stack-addresses                 | output as table with stack addresses
  --output forward:tcp://user:pass@127.0.0.1:24224?tag=tracee    | output via the Forward protocol to 127.0.0.1 on port 24224 with the tag 'tracee' using TCP
  --output webhook:http://webhook:8080                           | output events to the webhook endpoint
  --output webhook:http://webhook:8080?timeout=5s                | output events to the webhook endpoint with a timeout of 5s

Use this flag multiple times to choose multiple output options
`
}

type PrepareOutputResult struct {
	TraceeConfig   *config.OutputConfig
	PrinterConfigs []config.PrinterConfig
}

func PrepareOutput(outputSlice []string, newBinary bool) (PrepareOutputResult, error) {
	outConfig := PrepareOutputResult{}
	traceeConfig := &config.OutputConfig{}

	// outpath:format
	printerMap := make(map[string]string)

	for _, o := range outputSlice {
		outputParts := strings.SplitN(o, ":", 2)

		if strings.HasPrefix(outputParts[0], "gotemplate=") {
			err := parseFormat(outputParts, printerMap, newBinary)
			if err != nil {
				return outConfig, err
			}
			continue
		}

		switch outputParts[0] {
		case "none":
			if len(outputParts) > 1 {
				if newBinary {
					return outConfig, errors.New("none output does not support path. Use '--help output' for more info")
				}

				return outConfig, errors.New("none output does not support path. Use '--output help' for more info")
			}
			printerMap["stdout"] = "ignore"
		case "table", "table-verbose", "json", "gob":
			err := parseFormat(outputParts, printerMap, newBinary)
			if err != nil {
				return outConfig, err
			}
		case "forward":
			err := validateURL(outputParts, "forward", newBinary)
			if err != nil {
				return outConfig, err
			}

			printerMap[outputParts[1]] = "forward"
		case "webhook":
			err := validateURL(outputParts, "webhook", newBinary)
			if err != nil {
				return outConfig, err
			}

			printerMap[outputParts[1]] = "webhook"
		case "option":
			err := parseOption(outputParts, traceeConfig, newBinary)
			if err != nil {
				return outConfig, err
			}
		default:
			if newBinary {
				return outConfig, fmt.Errorf("invalid output flag: %s, use '--help output' for more info", outputParts[0])
			}

			return outConfig, fmt.Errorf("invalid output flag: %s, use '--output help' for more info", outputParts[0])
		}
	}

	// default
	if len(printerMap) == 0 {
		printerMap["stdout"] = "table"
	}

	printerConfigs, err := getPrinterConfigs(printerMap, traceeConfig, newBinary)
	if err != nil {
		return outConfig, err
	}

	outConfig.TraceeConfig = traceeConfig
	outConfig.PrinterConfigs = printerConfigs

	return outConfig, nil
}

// setOption sets the given option in the given config
func setOption(cfg *config.OutputConfig, option string, newBinary bool) error {
	switch option {
	case "stack-addresses":
		cfg.StackAddresses = true
	case "exec-env":
		cfg.ExecEnv = true
	case "relative-time":
		cfg.RelativeTime = true
	case "exec-hash":
		cfg.ExecHash = true
	case "parse-arguments":
		cfg.ParseArguments = true
	case "parse-arguments-fds":
		cfg.ParseArgumentsFDs = true
		cfg.ParseArguments = true // no point in parsing file descriptor args only
	case "sort-events":
		cfg.EventsSorting = true
	default:
		if newBinary {
			return errfmt.Errorf("invalid output option: %s, use '--help output' for more info", option)
		}

		return errfmt.Errorf("invalid output option: %s, use '--output help' for more info", option)
	}

	return nil
}

// getPrinterConfigs returns a slice of printer.Configs based on the given printerMap
func getPrinterConfigs(printerMap map[string]string, traceeConfig *config.OutputConfig, newBinary bool) ([]config.PrinterConfig, error) {
	printerConfigs := make([]config.PrinterConfig, 0, len(printerMap))

	for outPath, printerKind := range printerMap {
		if printerKind == "table" {
			if err := setOption(traceeConfig, "parse-arguments", newBinary); err != nil {
				return nil, err
			}
		}

		outFile := os.Stdout
		var err error

		if outPath != "stdout" && printerKind != "forward" && printerKind != "webhook" {
			outFile, err = createFile(outPath)
			if err != nil {
				return nil, err
			}
		}

		printerConfigs = append(printerConfigs, config.PrinterConfig{
			Kind:       printerKind,
			OutPath:    outPath,
			OutFile:    outFile,
			RelativeTS: traceeConfig.RelativeTime,
		})
	}

	return printerConfigs, nil
}

// parseFormat parses the given format and sets it in the given printerMap
func parseFormat(outputParts []string, printerMap map[string]string, newBinary bool) error {
	// if not file was passed, we use stdout
	if len(outputParts) == 1 {
		outputParts = append(outputParts, "stdout")
	}

	for _, outPath := range strings.Split(outputParts[1], ",") {
		if outPath == "" {
			if newBinary {
				return errfmt.Errorf("format flag can't be empty, use '--help output' for more info")
			}

			return errfmt.Errorf("format flag can't be empty, use '--output help' for more info")
		}

		if _, ok := printerMap[outPath]; ok {
			if newBinary {
				return errfmt.Errorf("cannot use the same path for multiple outputs: %s, use '--help output' for more info", outPath)
			}

			return errfmt.Errorf("cannot use the same path for multiple outputs: %s, use '--output help' for more info", outPath)
		}
		printerMap[outPath] = outputParts[0]
	}

	return nil
}

// parseOption parses the given option and sets it in the given config
func parseOption(outputParts []string, traceeConfig *config.OutputConfig, newBinary bool) error {
	if len(outputParts) == 1 || outputParts[1] == "" {
		if newBinary {
			return errfmt.Errorf("option flag can't be empty, use '--help output' for more info")
		}

		return errfmt.Errorf("option flag can't be empty, use '--output help' for more info")
	}

	for _, option := range strings.Split(outputParts[1], ",") {
		err := setOption(traceeConfig, option, newBinary)
		if err != nil {
			return err
		}
	}

	return nil
}

// creates *os.File for the given path
func createFile(path string) (*os.File, error) {
	fileInfo, err := os.Stat(path)
	if err == nil && fileInfo.IsDir() {
		return nil, errfmt.Errorf("cannot use a path of existing directory %s", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errfmt.Errorf("failed to create directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, errfmt.Errorf("failed to create output path: %v", err)
	}

	return file, nil
}

// validateURL validates the given URL
// --output [webhook|forward]:[protocol://user:pass@]host:port[?k=v#f]
func validateURL(outputParts []string, flag string, newBinary bool) error {
	if len(outputParts) == 1 || outputParts[1] == "" {
		if newBinary {
			return errfmt.Errorf("%s flag can't be empty, use '--help output' for more info", flag)
		}

		return errfmt.Errorf("%s flag can't be empty, use '--output help' for more info", flag)
	}
	// Now parse our URL using the standard library and report any errors from basic parsing.
	_, err := url.ParseRequestURI(outputParts[1])

	if err != nil {
		if newBinary {
			return errfmt.Errorf("invalid uri for %s output %q. Use '--help output' for more info", flag, outputParts[1])
		}

		return errfmt.Errorf("invalid uri for %s output %q. Use '--output help' for more info", flag, outputParts[1])
	}

	return nil
}
