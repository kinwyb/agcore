package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kinwyb/agcore/types"
)

type markitDown struct {
	pyFilePath string
}

// NewMarkitDownParseTool markitdown 解析工具
func NewMarkitDownParseTool(pyFile string) types.Tool {
	return &markitDown{pyFilePath: pyFile}
}

func (m *markitDown) Name() string {
	return "markitdown_read_file"
}

func (m *markitDown) Description() string {
	return "当用需要读区分析 PDF、Word(.docx)、Excel(.xlsx)、PPT、以及图片时，工具会自动将它们转换为带有结构标记的 Markdown 文本返回。"
}

func (m *markitDown) Parameters() map[string]any {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file": map[string]interface{}{
				"type":        "string",
				"description": "需要解析的文件绝对路径",
			},
		},
		"required": []string{"file"},
	}
}

func (m *markitDown) Execute(ctx context.Context, params map[string]any) (string, error) {
	file, ok := params["file"].(string)
	if !ok {
		return "", errors.New("请提供解析文件的路径")
	}
	if m.pyFilePath == "" {
		m.pyFilePath = m.defaultPyFile()
	}
	if m.pyFilePath == "" {
		return "", errors.New("markitdown解析的python脚本不存在")
	}
	// 1. 准备执行命令: python3 <脚本路径> <目标文件路径>
	cmd := exec.Command("python3", m.pyFilePath, file)
	// 2. 用 bytes.Buffer 捕获子进程的输出和错误
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 3. 执行命令并等待它结束
	err := cmd.Run()
	if err != nil {
		// 如果 Python 脚本报错，返回详细的错误信息
		return "", fmt.Errorf("python 脚本执行失败: %v, 错误详情: %s", err, strings.TrimSpace(stderr.String()))
	}
	// 4. 返回 Python 打印出来的 Markdown 文本
	return stdout.String(), nil
}

func (m *markitDown) defaultPyFile() string {
	execFile, _ := filepath.Abs(".")
	file := filepath.Join(execFile, "scripts", "mdConvert.py")
	if _, err := os.Stat(file); err == nil {
		return file
	}
	pyData := `import os
import sys

from openai import OpenAI

from markitdown import MarkItDown


def parse_and_read_file(file_path: str) -> str:
    """
    统一的文件读取工具。支持纯文本、PDF、Docx、Xlsx、PPTX 等。
    自动将复杂格式转换为大模型易读的 Markdown 文本。
    """
    if not os.path.exists(file_path):
        return f"错误：文件 {file_path} 不存在。"

    # 获取文件后缀
    _, ext = os.path.splitext(file_path.lower())

    # 1. 如果是纯文本或Markdown，直接读取
    if ext in ['.txt', '.md', '.json', '.csv', '.py', '.js']:
        try:
            with open(file_path, 'r', encoding='utf-8') as f:
                return f.read()
        except UnicodeDecodeError:
            with open(file_path, 'r', encoding='gbk') as f:
                return f.read()

    client = OpenAI(
        api_key=os.getenv("OPENAI_API_KEY"),
        base_url=os.getenv("OPENAI_BASE_URL"),
    )

    # 2. 如果是复杂文件格式，调用 MarkItDown 解析
    complex_extensions = ['.pdf', '.docx', '.xlsx', '.pptx', '.jpg', '.png']
    if ext in complex_extensions:
        try:
            md = MarkItDown(enable_plugins=True, llm_client=client, llm_model="qwen3.6-flash")
            result = md.convert(file_path)
            return result.text_content
        except Exception as e:
            return f"文件解析失败（格式：{ext}），错误原因：{str(e)}"

    return f"不支持的文件格式：{ext}。请提示用户转换格式后再试。"


def main():
    # sys.argv[0] 是脚本名字本身
    # sys.argv[1] 开始才是你后面传入的参数
    if len(sys.argv) < 2:
        print("错误：请提供要解析的文件路径！")
        print("用法：python3 test.py <文件路径>")
        sys.exit(1)

    # 获取终端传进来的第一个参数（即文件路径）
    file_path = sys.argv[1]
    result = parse_and_read_file(file_path)
    print("\n--- 解析结果如下 ---")
    print(result)

if __name__ == "__main__":
    main()
`
	err := os.WriteFile(file, []byte(pyData), 0777)
	if err == nil {
		return file
	}
	return ""
}
