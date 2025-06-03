// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"k8s.io/klog/v2"
)

func init() {
	RegisterTool(&BashTool{})
}

const (
	defaultBashBin = "/bin/bash"
)

// Find the bash executable path using exec.LookPath.
// On some systems (like NixOS), executables might not be in standard locations like /bin/bash.
func lookupBashBin() string {
	actualBashPath, err := exec.LookPath("bash")
	if err != nil {
		klog.Warningf("'bash' not found in PATH, defaulting to %s: %v", defaultBashBin, err)
		return defaultBashBin
	}
	return actualBashPath
}

// expandShellVar expands shell variables and syntax using bash
func expandShellVar(value string) (string, error) {
	if strings.Contains(value, "~") {
		if len(value) >= 2 && value[0] == '~' && os.IsPathSeparator(value[1]) {
			if runtime.GOOS == "windows" {
				value = filepath.Join(os.Getenv("USERPROFILE"), value[2:])
			} else {
				value = filepath.Join(os.Getenv("HOME"), value[2:])
			}
		}
	}
	return os.ExpandEnv(value), nil
}

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	return "Executes a bash command. Use this tool only when you need to execute a shell command."
}

func (t *BashTool) FunctionDefinition() *gollm.FunctionDefinition {
	return &gollm.FunctionDefinition{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &gollm.Schema{
			Type: gollm.TypeObject,
			Properties: map[string]*gollm.Schema{
				"command": {
					Type:        gollm.TypeString,
					Description: `The bash command to execute.`,
				},
				"modifies_resource": {
					Type: gollm.TypeString,
					Description: `Whether the command modifies a kubernetes resource.
Possible values:
- "yes" if the command modifies a resource
- "no" if the command does not modify a resource
- "unknown" if the command's effect on the resource is unknown
`,
				},
			},
		},
	}
}

func (t *BashTool) Run(ctx context.Context, args map[string]any) (any, error) {
	kubeconfig := ctx.Value(KubeconfigKey).(string)
	workDir := ctx.Value(WorkDirKey).(string)
	command := args["command"].(string)

	if strings.Contains(command, "kubectl edit") {
		return &ExecResult{Error: "interactive mode not supported for kubectl, please use non-interactive commands"}, nil
	}
	if strings.Contains(command, "kubectl port-forward") {
		return &ExecResult{Error: "port-forwarding is not allowed because assistant is running in an unattended mode, please try some other alternative"}, nil
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, os.Getenv("COMSPEC"), "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, lookupBashBin(), "-c", command)
	}
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	if kubeconfig != "" {
		kubeconfig, err := expandShellVar(kubeconfig)
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "KUBECONFIG="+kubeconfig)
	}

	return executeCommand(cmd)
}

type ExecResult struct {
	Error      string `json:"error,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	StreamType string `json:"stream_type,omitempty"`
}

func IsInteractiveCommand(command string) (bool, error) {
	// Inline isKubectlCommand logic
	words := strings.Fields(command)
	if len(words) == 0 {
		return false, nil
	}
	base := filepath.Base(words[0])
	if base != "kubectl" {
		return false, nil
	}

	isExec := strings.Contains(command, " exec ") && strings.Contains(command, " -it")
	isPortForward := strings.Contains(command, " port-forward ")
	isEdit := strings.Contains(command, " edit ")

	if isExec || isPortForward || isEdit {
		return true, fmt.Errorf("interactive mode not supported for kubectl, please use non-interactive commands")
	}
	return false, nil
}

func executeCommand(cmd *exec.Cmd) (*ExecResult, error) {
	command := strings.Join(cmd.Args, " ")

	if isInteractive, err := IsInteractiveCommand(command); isInteractive {
		return &ExecResult{Error: err.Error()}, nil
	}

	isWatch := strings.Contains(command, " get ") && strings.Contains(command, " -w")
	isLogs := strings.Contains(command, " logs ") && strings.Contains(command, " -f")
	isAttach := strings.Contains(command, " attach ")

	// Handle streaming commands
	if isWatch || isLogs || isAttach {
		// Create a context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()

		// Create pipes for stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("creating stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("creating stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("starting command: %w", err)
		}

		// Read output in goroutines
		var stdoutBuilder, stderrBuilder strings.Builder
		stdoutDone := make(chan struct{})
		stderrDone := make(chan struct{})
		isTimeout := false

		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if isTimeout {
					// Stop reading if timeout occurred
					return
				}
				line := scanner.Text() + "\n"
				fmt.Print(line)
				stdoutBuilder.WriteString(line)
			}
			close(stdoutDone)
		}()

		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				if isTimeout {
					// Stop reading if timeout occurred
					return
				}
				line := scanner.Text() + "\n"
				fmt.Fprint(os.Stderr, line)
				stderrBuilder.WriteString(line)
			}
			close(stderrDone)
		}()

		// Wait for either timeout or command completion
		select {
		case <-ctx.Done():
			isTimeout = true
			// Kill the process immediately on timeout
			if cmd.Process != nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
			// Return timeout message to be displayed via UI
			return &ExecResult{
				Error:      "Timeout reached after 7 seconds",
				Stdout:     stdoutBuilder.String(),
				Stderr:     stderrBuilder.String(),
				StreamType: "timeout",
			}, nil
		case <-stdoutDone:
			<-stderrDone // Wait for stderr to finish too
		}

		// Ensure the command is terminated
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}

		results := &ExecResult{
			Stdout: stdoutBuilder.String(),
			Stderr: stderrBuilder.String(),
		}
		if isWatch {
			results.StreamType = "watch"
		} else if isLogs {
			results.StreamType = "logs"
		} else if isAttach {
			results.StreamType = "attach"
		}
		return results, nil
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	results := &ExecResult{}
	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			results.ExitCode = exitError.ExitCode()
		} else {
			return nil, err
		}
	}
	results.Stdout = stdout.String()
	results.Stderr = stderr.String()
	return results, nil
}

func (t *BashTool) IsInteractive(args map[string]any) (bool, error) {
	commandVal, ok := args["command"]
	if !ok || commandVal == nil {
		return false, nil
	}

	command, ok := commandVal.(string)
	if !ok {
		return false, nil
	}

	return IsInteractiveCommand(command)
}
