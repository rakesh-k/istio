// Copyright 2019 Istio Authors
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

package istioio

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"istio.io/istio/pkg/test/scopes"
)

var (
	// Matcher for links to the Istio github repository.
	githubLinkMatch = regexp.MustCompile("@.*@")
)

var _ Step = Command{}

// Command is a Step for running a Bash command. Generates the following snippets:
//
// {Input.Name()}: The snippet for the command.
// {Input.Name()}_output.txt: The snippet for the output of the command.
type Command struct {
	Input   InputSelector
	Verify  Verifier
	WorkDir string
	Env     map[string]string

	// CreateSnippet if true, creates a snippet for the command.
	CreateSnippet bool

	// IncludeOutputInSnippet if true, includes the output of the command in the generated snippet. Only applies
	// if CreateSnippet=true.
	IncludeOutputInSnippet bool

	// CreateOutputSnippet if true, creates a separate snippet for the output of the command, suffixed with "_output".
	CreateOutputSnippet bool
}

func (s Command) run(ctx Context) {
	// Copy the script to the output directory.
	input := s.Input.SelectInput(ctx)
	dir, fileName := filepath.Split(input.Name())

	scopes.CI.Infof("Running command %s", input.Name())

	// Read the snippet for the command.
	snippetContent, err := input.ReadAll()
	if err != nil {
		ctx.Fatal("failed reading input command %s: %v", input.Name(), err)
	}
	snippetContent = strings.TrimSpace(snippetContent)

	// Apply a filter to generate the runnable command.
	commandContent := filterCommand(snippetContent)

	// Copy the command to workDir.
	if err := ioutil.WriteFile(path.Join(ctx.WorkDir(), fileName+".txt"), []byte(commandContent), 0644); err != nil {
		ctx.Fatalf("failed copying command %s to workDir: %v", input.Name(), err)
	}

	// Create the command.
	cmd := exec.Command("bash")
	cmd.Dir = s.getWorkDir(ctx, dir)
	cmd.Env = s.getEnv(ctx)
	cmd.Stdin = strings.NewReader(commandContent)

	// Run the command and get the output.
	output, err := cmd.CombinedOutput()
	output = bytes.TrimSpace(output)

	// Copy the command output to workDir
	outputFileName := fileName + "_output.txt"
	if err := ioutil.WriteFile(filepath.Join(ctx.WorkDir(), outputFileName), output, 0644); err != nil {
		ctx.Fatalf("failed copying output for command %s: %v", input.Name(), err)
	}

	// Verify the results.
	scopes.CI.Infof("Verifying results of command %s", input.Name())
	s.getVerifier()(ctx, input.Name(), string(output), err)

	// Generate the snippet, if specified.
	if s.CreateSnippet {
		outputIs := ""

		// Add the output, if specified.
		if s.IncludeOutputInSnippet {
			outputIs = "text"
			snippetContent += "\n" + string(output)
		}

		// Generate the snippet.
		Snippet{
			Syntax:   "bash",
			OutputIs: outputIs,
			Input: Inline{
				FileName: fileName,
				Value:    snippetContent,
			},
		}.run(ctx)
	}

	if s.CreateOutputSnippet {
		// Generate a separate snippet for the output.
		Snippet{
			Name: fileName + "_output",
			Input: Inline{
				Value: string(output),
			},
		}.run(ctx)
	}
}

type commandFilter func(string) (bool, string)

// filterCommand scrubs the given command script so that it is ready for execution.
func filterCommand(content string) string {
	// Remove surrounding @'s for github links.
	content = githubLinkMatch.ReplaceAllStringFunc(content, func(input string) string {
		return input[1 : len(input)-1]
	})

	lines := strings.Split(content, "\n")
	out := ""

	for i, line := range lines {
		// Apply the filters in order.
		include := true
		for _, filter := range []commandFilter{noShellPrompt, noCommentLines, noEmptyLines} {
			include, line = filter(line)
			if !include {
				break
			}
		}
		if !include {
			continue
		}

		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}

func noEmptyLines(line string) (include bool, result string) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) == 0 {
		return false, ""
	}
	return true, line
}

func noCommentLines(line string) (include bool, result string) {
	if len(line) > 0 && line[0] == '#' {
		return false, ""
	}
	return true, line
}

func noShellPrompt(line string) (include bool, result string) {
	return true, strings.TrimPrefix(line, "$ ")
}

func (s Command) getWorkDir(ctx Context, scriptDir string) string {
	if s.WorkDir != "" {
		// User-specified work dir for the script.
		return s.WorkDir
	}
	// By default, run the command from the scripts dir in case one script calls another.
	scriptDir, err := filepath.Abs(scriptDir)
	if err != nil {
		ctx.Fatalf("failed resolving absolute path for %s", scriptDir)
	}
	return scriptDir
}

func (s Command) getEnv(ctx Context) []string {
	// Start with the environment for the current process.
	e := os.Environ()

	// Copy the user-specified environment (if set) and add the k8s config.
	customVars := map[string]string{
		"KUBECONFIG": ctx.Env.Settings().KubeConfig,
	}
	for k, v := range s.Env {
		customVars[k] = v
	}

	// Append the custom vars  to the list.
	for name, value := range customVars {
		e = append(e, fmt.Sprintf("%s=%s", name, value))
	}
	return e
}

func (s Command) getVerifier() Verifier {
	if s.Verify != nil {
		return s.Verify
	}
	return defaultVerifier
}

// SleepCommand returns a command that runs `sleep <delay>`
func SleepCommand(delay int) Command {
	return Command{
		Input: Inline{
			Value:    fmt.Sprintf("sleep %d", delay),
			FileName: "sleep",
		},
	}
}
