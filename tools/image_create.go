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

	"github.com/kinwyb/agcore/types"
)

type imageCreate struct {
	pyFilePath string
}

// NewImageCreateTool 图片生成工具
func NewImageCreateTool(pyFile string) types.Tool {
	return &imageCreate{pyFilePath: pyFile}
}

func (m *imageCreate) Name() string {
	return "image_create"
}

func (m *imageCreate) Description() string {
	return "当用户请求生成、绘制、创作、设计、修改新图片，或者需要将文字描述转化为视觉图像时，调用此工具。工具会根据详细的英文或中文描述生成一张高清图片。"
}

func (m *imageCreate) Parameters() map[string]any {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "用于生成图片的文本提示词。应当尽可能详细，包含主体、背景、风格（如：写实、动漫、赛博朋克、水墨画）、色彩基调、构图等。大模型在调用前应自动对用户的简单请求进行丰富和扩写，以达到最佳构图效果。",
			},
			"style": map[string]any{
				"type":        "string",
				"description": "图片生成的渲染风格描述",
			},
			"size": map[string]any{
				"type":        "string",
				"enum":        []string{"2688*1536", "1536*2688", "2048*2048", "2048x2048", "2368*1728"},
				"default":     "2048*2048",
				"description": "生成图片的宽高分辨率。如果不指定，默认使用 '2048*2048'",
			},
		},
		"required": []string{"prompt"},
	}
}

func (m *imageCreate) Execute(ctx context.Context, params map[string]any) (string, error) {
	file, ok := params["prompt"].(string)
	if !ok {
		return "", errors.New("请提供")
	}
	if m.pyFilePath == "" {
		m.pyFilePath = m.defaultPyFile()
	}
	if m.pyFilePath == "" {
		return "", errors.New("图片生成的python脚本不存在")
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
	jsonData := stdout.String()
	// 2. 反序列化到结构体中
	var resp imageOpenAIResponse
	err = json.Unmarshal([]byte(jsonData), &resp)
	if err != nil {
		return jsonData, err
	}
	// 3. 安全地遍历并提取图片地址
	var images []string
	for _, choice := range resp.Choices {
		for _, item := range choice.Message.Content {
			// 确保类型是 image_url 且 url 不为空
			if item.Type == "image_url" && item.ImageURL.URL != "" {
				images = append(images, item.ImageURL.URL)
			}
		}
	}
	if len(images) == 0 {
		return jsonData, nil
	}
	resultData, _ := json.Marshal(images)
	return string(resultData), nil
}

func (m *imageCreate) defaultPyFile() string {
	execFile, _ := filepath.Abs(".")
	file := filepath.Join(execFile, "scripts", "imageBuilder.py")
	if _, err := os.Stat(file); err == nil {
		return file
	}
	pyData := `
import datetime
import json
import sys
import uuid
import os

from openai import OpenAI

def convert_to_openai_format(custom_resp: str) -> str:
    """将自定义的自定义响应格式转换为 OpenAI 标准输出格式"""
    # ✨ 核心修复：如果是字符串，先解析为字典
    if isinstance(custom_resp, str):
        try:
            custom_resp = json.loads(custom_resp)
        except json.JSONDecodeError:
            raise ValueError(
                "输入的字符串不是合法的 JSON 格式，请检查输入！"
            )
    # 1. 安全地提取原始数据中的信息
    output_data = custom_resp.get("output", {})
    choices_list = output_data.get("choices", [])

    openai_choices = []

    # 2. 遍历并重构 choices
    for index, choice in enumerate(choices_list):
        orig_message = choice.get("message", {})
        orig_content = orig_message.get("content", [])

        # 处理多模态或文本内容
        # OpenAI 标准格式中，如果是纯文本，content 是字符串；如果是多模态，可以是列表
        formatted_content = ""
        if isinstance(orig_content, list):
            # 如果内容里有图片链接，保留其多模态结构或转为标准 OpenAI 格式
            formatted_content = []
            for item in orig_content:
                if "image" in item:
                    formatted_content.append(
                        {
                            "type": "image_url",
                            "image_url": {"url": item["image"]},
                        }
                    )
                elif "text" in item:
                    formatted_content.append(
                        {"type": "text", "text": item["text"]}
                    )
        else:
            formatted_content = orig_content

        # 构建标准的 choice 对象
        openai_choice = {
            "index": index,
            "message": {
                "role": orig_message.get("role", "assistant"),
                "content": formatted_content,
            },
            "logprobs": None,
            "finish_reason": choice.get("finish_reason", "stop"),
        }
        openai_choices.append(openai_choice)

        # 3. 组装成 OpenAI 完整的标准 Response 字典
        openai_response = {
            "id": custom_resp.get("request_id")
            if custom_resp.get("request_id")
            else f"chatcmpl-{uuid.uuid4()}",
            "object": "chat.completion",
            "created": int(datetime.datetime.now().timestamp()),  # 如果原数据没有，默认赋予当前时间戳
            "model": custom_resp.get("model") or "custom-model",
            "choices": openai_choices,
            "usage": custom_resp.get("usage")
            if custom_resp.get("usage")
            else {
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "total_tokens": 0,
            },
            "system_fingerprint": custom_resp.get("system_fingerprint"),
        }

    return json.dumps(openai_response, indent=2, ensure_ascii=False)

def build_image(image_desc: str) -> str:
    """
    图片生成，imageDesc要生成的图片描述
    """

    # 以下为华北2（北京）地域的URL，各地域的URL不同。
    base_http_api_url = os.getenv("OPENAI_BASE_URL")
    messages = [
        {
            "role": "user",
            "content": [
                {"text": image_desc}
            ]
        }
    ]

    # 新加坡和北京地域的API Key不同。获取API Key：https://help.aliyun.com/zh/model-studio/get-api-key
    # 若没有配置环境变量，请用百炼API Key将下行替换为：api_key="sk-xxx"
    api_key = os.getenv("OPENAI_API_KEY")

    client = OpenAI(
        # 若没有配置环境变量，请用百炼API Key将下行替换为：api_key="sk-xxx"
        api_key=api_key,
        base_url=base_http_api_url,
    )

    completion = client.chat.completions.create(
        model="qwen-image-2.0",
        messages=messages,
    )
    dump = completion.model_dump_json()
    return convert_to_openai_format(dump)


def main():
    # sys.argv[0] 是脚本名字本身
    # sys.argv[1] 开始才是你后面传入的参数
    if len(sys.argv) < 2:
        print("错误：提供要生成图片的要求内容！")
        print("用法：python3 imageBuilder.py <文件路径>")
        sys.exit(1)

    # 获取终端传进来的第一个参数（即图片生成的要求内容）
    image_desc = sys.argv[1]
    result = build_image(image_desc)
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

// 1. 根据 OpenAI 结构定义需要提取的专属结构体
type imageOpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}
