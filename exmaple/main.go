package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
	"github.com/taosu0216/mcpX"
)

const mcpConfigPath = "config.json"

func main() {
	ctx := context.Background()

	config := &openai.ChatModelConfig{
		APIKey:  "sk-****",
		Model:   "gpt4",
		BaseURL: "",
	}
	baseModel, err := openai.NewChatModel(ctx, config)
	if err != nil {
		panic(err)
	}
	allDialogTools := []*schema.ToolInfo{}

	mcpServers, err := mcpX.LoadConfig(mcpConfigPath)
	if err != nil {
		panic(err)
	}
	for _, server := range mcpServers.Servers {
		for _, tool := range server.Tools {
			allDialogTools = append(allDialogTools, tool.ToolInfo)
		}
	}
	chatModel, err := baseModel.WithTools(allDialogTools)
	if err != nil {
		panic(err)
	}
	dialog := []*schema.Message{
		{
			Role:    "user",
			Content: "调用 context7 的 resolve_library_id 方法，帮我搜一下 go 的 eino 框架相关的使用 sdk,直接搜素 eino 就可以，不需要管别的",
		},
	}
	resp, err := chatModel.Generate(ctx, dialog)
	if err != nil {
		panic(err)
	}
	if len(resp.ToolCalls) > 0 {
		toolCall := resp.ToolCalls[0] // 简化处理，只执行第一个工具调用
		var toolResult string
		var toolErr error
		log.Println("Agent: 决定使用工具: ", toolCall.Function.Name)

		// 3. 执行工具
		switch toolCall.Function.Name {
		case "resolve-library-id":
			fn := mcpX.McpToolName2Fn[toolCall.Function.Name]
			if fn == nil {
				panic(fmt.Sprintf("unknown tool: %s", toolCall.Function.Name))
			}
			toolResult, toolErr = fn(ctx, toolCall.Function.Arguments)
		default:
			toolResult = fmt.Sprintf("未知的工具: %s", toolCall.Function.Name)
			toolErr = errors.New("unknown tool")
		}

		if toolErr != nil {
			log.Printf("❗️ Agent: 工具执行出错: %v", toolErr)
		}

		// 4. 将工具执行结果作为新的信息，再次喂给LLM进行总结
		log.Println("Agent: 已获得工具结果，正在进行总结...")
		log.Println(toolResult)
	} else {
		log.Println("Agent: 未获得工具调用，直接返回结果...")
		log.Println(resp.Content)
	}
}
