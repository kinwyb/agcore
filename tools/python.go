package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	pythonDefaultImage   = "agent-sandbox:latest"
	pythonMaxOutputSize  = 10 * 1024 // 10KB
	pythonDefaultTimeout = 30        // seconds
)

// PythonTool executes Python code inside an isolated Docker container
type PythonTool struct {
	timeout        int
	image          string
	dockerfilePath string
	workspace      string
	outputPath     string
}

// NewPythonTool creates a Python execution tool running in Docker sandbox
func NewPythonTool(timeoutSeconds int, workspace string, output string) *PythonTool {
	if timeoutSeconds <= 0 {
		timeoutSeconds = pythonDefaultTimeout
	}
	if output == "" {
		output = filepath.Join(workspace, "tmp")
	}
	return &PythonTool{
		timeout:        timeoutSeconds,
		image:          pythonDefaultImage,
		dockerfilePath: findDockerfilePath(),
		workspace:      workspace,
		outputPath:     output,
	}
}

// findDockerfilePath locates the sandbox Dockerfile relative to this source file
func findDockerfilePath() string {
	// 1. 获取当前正在执行的二进制文件的绝对路径
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exePath)
	return filepath.Join(dir, "sandbox", "Dockerfile")
}

// Name returns the tool name
func (t *PythonTool) Name() string {
	return "python_execute"
}

// Description returns the tool description
func (t *PythonTool) Description() string {
	return `Execute Python code in an isolated Docker sandbox container. Use this tool for: complex math calculations, data analysis (pandas, numpy), file processing, table data extraction, running specific algorithms, or any task requiring Python execution.

The code runs in a sandboxed container with:
- Resource limits (512MB memory, 1 CPU)
- Read-only Input: The input files are located in /workspace (which maps to the host's ` + t.workspace + `).
- Auto-destruction after execution (--rm)

### ⚠️ Critical File Output Rules
Because the Docker container is ephemeral and **destroyed automatically immediately after execution**, any files saved to relative paths or default directories will be permanently lost.
To persist and retrieve any generated files (e.g., CSVs, Images, JSON, TXT), you **MUST** save them directly into the /out directory using its absolute path.

* ❌ **Incorrect:** df.to_csv("result.csv") or plt.savefig("chart.png") *(Files will be lost upon container destruction)*
* ✅ **Correct:** df.to_csv("/out/result.csv") or plt.savefig("/out/chart.png") *(Files will be safely persisted)*

> ℹ️ **Host Mapping Note:** The container's /out directory is mapped to the host machine's ` + t.outputPath + ` directory. Once the execution finishes, the output files you saved can be retrieved from the host at ` + t.outputPath + `.

### Notes

* Network access is enabled, so you can install third-party packages via pip if necessary (e.g., import os; os.system("pip install package_name")).
* Pre-installed packages: pandas, numpy, matplotlib, openpyxl, scikit-learn, requests. No need to pip install these.
* Output is limited to 10KB for both stdout and stderr.
* Execution timeout is 30 seconds.`
}

// Parameters returns the JSON Schema for tool parameters
func (t *PythonTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Python code to execute",
			},
		},
		"required": []string{"code"},
	}
}

// ApprovalPrompt returns the approval prompt content
func (t *PythonTool) ApprovalPrompt(argsJSON string) string {
	code := argsJSON
	params := map[string]string{}
	if err := json.Unmarshal([]byte(argsJSON), &params); err == nil {
		if c, ok := params["code"]; ok {
			code = c
		}
	}
	preview := code
	if len(preview) > 300 {
		preview = preview[:300] + "\n... (代码已截断)"
	}
	return fmt.Sprintf("在 Docker 沙盒中执行以下 Python 代码（需要审批）：\n\n```python\n%s\n```\n\n是否批准执行？", preview)
}

// Execute runs Python code inside a Docker container
func (t *PythonTool) Execute(ctx context.Context, params map[string]interface{}) (string, error) {
	code, ok := params["code"].(string)
	if !ok || code == "" {
		return "", fmt.Errorf("code parameter is required")
	}

	// Check Docker availability
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker 不可用: docker 命令不在 PATH 中。请安装 Docker 后再使用此工具。")
	}

	// Ensure the sandbox image exists
	if err := t.ensureImage(ctx); err != nil {
		return "", fmt.Errorf("沙盒镜像准备失败: %w", err)
	}

	// Build working directory
	workDir := t.workspace
	outputPath := t.outputPath

	// Build docker run command
	args := []string{
		"run",
		"--rm",
		"-i", // interactive: allows stdin input for code
		"--memory=512m",
		"--cpus=1",
		"--tmpfs=/tmp:rw,noexec,nosuid,size=64m",
		"-v", workDir + ":/workspace:ro",
		"-v", outputPath + ":/out:rw",
		t.image,
		"python3", // reads code from stdin
	}

	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(t.timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "docker", args...)

	// Feed Python code via stdin to avoid shell escaping issues with complex code
	cmd.Stdin = strings.NewReader(code)

	// Capture output with size limits
	var stdoutBuf, stderrBuf pythonLimitWriter
	stdoutBuf.limit = pythonMaxOutputSize
	stderrBuf.limit = pythonMaxOutputSize
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	var exitCode int
	err := cmd.Run()
	if err != nil {
		// 优先检查是不是沙盒本身设定的时间到了
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return formatPythonResult(&stdoutBuf, &stderrBuf, -1), fmt.Errorf("python 执行超时（%d 秒）", t.timeout)
		}
		// 检查是不是外层链路主动叫停（比如父 context 被 cancel 了）
		if ctx.Err() != nil {
			return formatPythonResult(&stdoutBuf, &stderrBuf, -1), fmt.Errorf("任务已被主动取消: %w", ctx.Err())
		}
		if exitErr, ok2 := errors.AsType[*exec.ExitError](err); ok2 {
			exitCode = exitErr.ExitCode()
		}
	}

	return formatPythonResult(&stdoutBuf, &stderrBuf, exitCode), nil
}

// ensureImage checks if the sandbox image exists locally, builds from Dockerfile if not,
// and falls back to docker pull if the build fails.
func (t *PythonTool) ensureImage(ctx context.Context) error {
	// 1. Check if image exists locally
	checkCmd := exec.CommandContext(ctx, "docker", "image", "inspect", t.image)
	if err := checkCmd.Run(); err == nil {
		return nil // image already exists
	}

	// 2. Try to build from Dockerfile
	if t.dockerfilePath != "" {
		if err := t.buildImage(ctx); err == nil {
			return nil // build succeeded
		}
	} else {
		t.image = "python:3.10-slim"
	}

	// 3. Fallback: pull from registry
	return t.pullImage(ctx)
}

// buildImage builds the sandbox image from the local Dockerfile
func (t *PythonTool) buildImage(ctx context.Context) error {
	dockerDir := filepath.Dir(t.dockerfilePath)
	args := []string{"build", "-t", t.image, "-f", t.dockerfilePath, dockerDir}

	cmd := exec.CommandContext(ctx, "docker", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("构建沙盒镜像失败: %s", truncateString(output.String(), 200))
	}
	return nil
}

// pullImage pulls the sandbox image from a Docker registry
func (t *PythonTool) pullImage(ctx context.Context) error {
	pullCmd := exec.CommandContext(ctx, "docker", "pull", t.image)
	var pullOut bytes.Buffer
	pullCmd.Stdout = &pullOut
	pullCmd.Stderr = &pullOut
	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("拉取沙盒镜像 %s 失败: %s", t.image, truncateString(pullOut.String(), 200))
	}
	return nil
}

// formatPythonResult formats the execution result for the LLM
func formatPythonResult(stdout, stderr *pythonLimitWriter, exitCode int) string {
	var sb strings.Builder

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	if stdoutStr != "" {
		sb.WriteString(stdoutStr)
	}
	if stderrStr != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n--- stderr ---\n")
		}
		sb.WriteString(stderrStr)
	}
	if exitCode != 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("--- exit code: %d ---", exitCode))
	}
	if sb.Len() == 0 {
		sb.WriteString("(无输出)")
	}
	return sb.String()
}

// pythonLimitWriter captures output with a byte limit
type pythonLimitWriter struct {
	data  bytes.Buffer
	limit int
}

func (w *pythonLimitWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.data.Len()
	if remaining <= 0 {
		return len(p), nil // silently drop excess
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return w.data.Write(p)
}

func (w *pythonLimitWriter) String() string {
	return w.data.String()
}

func (w *pythonLimitWriter) Len() int {
	return w.data.Len()
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
