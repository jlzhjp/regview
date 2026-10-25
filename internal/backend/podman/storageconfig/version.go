package storageconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type version struct {
	major int
	text  string
}

var versionNumber = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func localVersion(ctx context.Context) (version, error) {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, "podman", "--remote=false", "version", "--format", "json")
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		if probe.Err() != nil {
			err = probe.Err()
		} else {
			var exit *exec.ExitError
			if errors.As(err, &exit) && len(exit.Stderr) != 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
			}
		}
		return version{}, fmt.Errorf("detect local Podman version: %w", err)
	}
	return parseVersion(string(output))
}

func parseVersion(output string) (version, error) {
	var report struct {
		Client struct {
			Version string `json:"Version"`
		} `json:"Client"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return version{}, fmt.Errorf("decode local Podman version JSON: %w", err)
	}
	match := versionNumber.FindStringSubmatch(report.Client.Version)
	if match == nil {
		return version{}, fmt.Errorf("invalid local Podman Client.Version %q", report.Client.Version)
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return version{}, fmt.Errorf("parse local Podman major version: %w", err)
	}
	return version{major: major, text: report.Client.Version}, nil
}
