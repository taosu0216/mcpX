package mcpX

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/viper"
)

type McpServerModels struct {
	Server map[string]*McpServerModel
}
type McpServerModel struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

var McpToolName2Fn map[string]func(ctx context.Context, params string) (string, error)

type McpXServers struct {
	Servers map[string]*McpXServer
}

type McpXServer struct {
	ServerName string
	Tools      map[string]*McpXTool
}

type McpXTool struct {
	ToolInfo *schema.ToolInfo
	Fn       func(ctx context.Context, params string) (string, error)
}

type McpToolSchema struct {
	Result  Result `json:"result"`
	Jsonrpc string `json:"jsonrpc"`
	ID      int    `json:"id"`
}
type ItemSchema struct {
	Type string `json:"type"`
}

type LibraryName struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Items       *ItemSchema `json:"items,omitempty"`
}
type InputSchema struct {
	Type                 string                 `json:"type"`
	Properties           map[string]LibraryName `json:"properties"`
	Required             []string               `json:"required"`
	AdditionalProperties bool                   `json:"additionalProperties"`
	Schema               string                 `json:"$schema"`
}
type Tools struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}
type Result struct {
	Tools []Tools `json:"tools"`
}

func LoadConfig(configPath string) (*McpXServers, error) {
	McpToolName2Fn = make(map[string]func(ctx context.Context, params string) (string, error))
	// 初始化 viper
	v := viper.New()
	v.SetConfigFile(configPath)

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析到 McpServers 结构体
	var servers McpServerModels
	var xServers McpXServers
	xServers.Servers = make(map[string]*McpXServer)

	if err := v.UnmarshalKey("mcpServers", &servers.Server); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	for serverName, serverModel := range servers.Server {
		xServers.Servers[serverName] = ConvertMcpServerModel2McpXServer(serverName, serverModel)
		for _, tool := range xServers.Servers[serverName].Tools {
			McpToolName2Fn[tool.ToolInfo.Name] = tool.Fn
		}
	}

	return &xServers, nil
}

func GetFnByName(name string) (func(ctx context.Context, params string) (string, error), error) {
	fn, ok := McpToolName2Fn[name]
	if !ok {
		return nil, fmt.Errorf("tool %s not found", name)
	}
	return fn, nil
}

func ConvertMcpServerModel2McpXServer(serverName string, serverModel *McpServerModel) *McpXServer {
	argCh := make(chan string)
	respCh := make(chan string, 100)
	toolSchemaCh := make(chan string, 1)

	go func() {
		// 创建初始命令
		cmd := exec.Command(serverModel.Command, serverModel.Args...)

		// 设置环境变量
		cmd.Env = os.Environ()
		for k, v := range serverModel.Env {
			// 将环境变量名转换为大写
			k = strings.ToUpper(k)
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}

		// 创建输入输出管道
		stdin, err := cmd.StdinPipe()
		if err != nil {
			fmt.Printf("创建stdin管道失败: %v\n", err)
			return
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Printf("创建stdout管道失败: %v\n", err)
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			fmt.Printf("创建stderr管道失败: %v\n", err)
			return
		}

		// 启动命令
		if err = cmd.Start(); err != nil {
			fmt.Printf("启动命令失败: %v\n", err)
			return
		}

		// a goroutine to read from stderr
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				fmt.Printf("mcp server stderr: %s\n", scanner.Text())
			}
		}()
		toolDescArg := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
		_, err = stdin.Write([]byte(toolDescArg))
		if err != nil {
			return
		}
		reader := bufio.NewReader(stdout)
		toolRespStr, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("从stdout读取失败: %v\n", err)
			return
		}
		toolSchemaCh <- toolRespStr
		close(toolSchemaCh)

		// 处理输入
		go func() {
			for command := range argCh {
				if _, err = stdin.Write([]byte(command)); err != nil {
					fmt.Printf("写入命令失败: %v\n", err)
					return
				}
			}
		}()

		// 处理输出
		buffer := make([]byte, 1024)
		for {
			n, err := stdout.Read(buffer)
			if n > 0 {
				respCh <- string(buffer[:n])
			}
			if err != nil {
				break
			}
		}

		// 等待命令结束
		if err = cmd.Wait(); err != nil {
			fmt.Printf("命令执行失败: %v\n", err)
		}
	}()

	var toolSchemaObj McpToolSchema
	for toolSchema := range toolSchemaCh {
		if err := json.Unmarshal([]byte(toolSchema), &toolSchemaObj); err != nil {
			return nil
		}
	}

	xServer := McpXServer{
		ServerName: serverName,
	}
	xServer.Tools = convertMcpToolSchemaToTools(&toolSchemaObj, argCh, respCh)

	return &xServer
}

func convertMcpToolSchemaToTools(toolSchema *McpToolSchema, argCh chan<- string, respCh <-chan string) map[string]*McpXTool {
	tools := make(map[string]*McpXTool)
	for i := 1; i < len(toolSchema.Result.Tools); i++ {
		tool := toolSchema.Result.Tools[i-1]
		tools[tool.Name] = &McpXTool{
			ToolInfo: &schema.ToolInfo{
				Name:        tool.Name,
				Desc:        tool.Description,
				ParamsOneOf: getParamsByInputSchema(tool.InputSchema),
			},
			Fn: func(ctx context.Context, params string) (string, error) {
				// The `params` string is expected to be a valid JSON structure (object or array).

				req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"%s","arguments":%s}}`, i+1, tool.Name, params)

				// Send request to the MCP process. Appending newline is often necessary.
				argCh <- req + "\n"

				// Wait for the response or context cancellation.
				select {
				case responseStr := <-respCh:
					// A more robust implementation would parse the JSON-RPC response here
					// to separate the result from potential errors.
					return responseStr, nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		}
	}
	return tools
}

func getParamsByInputSchema(inputSchema InputSchema) *schema.ParamsOneOf {
	properties := openapi3.Schemas{}
	for name, prop := range inputSchema.Properties {
		schemaValue := &openapi3.Schema{
			Type:        getOpenapi3Type(prop.Type),
			Description: prop.Description,
		}
		if getOpenapi3Type(prop.Type) == openapi3.TypeArray {
			// Google API requires an items definition for array types.
			// We'll default to string if not provided by the MCP server.
			itemType := openapi3.TypeString
			if prop.Items != nil && prop.Items.Type != "" {
				itemType = getOpenapi3Type(prop.Items.Type)
			}
			schemaValue.Items = &openapi3.SchemaRef{
				Value: &openapi3.Schema{
					Type: itemType,
				},
			}
		}
		properties[name] = &openapi3.SchemaRef{
			Value: schemaValue,
		}
	}

	toolSchema := &openapi3.Schema{
		Type:       openapi3.TypeObject,
		Properties: properties,
		Required:   inputSchema.Required,
	}

	return schema.NewParamsOneOfByOpenAPIV3(toolSchema)
}

func getOpenapi3Type(inputType string) string {
	switch inputType {
	case "string":
		return openapi3.TypeString
	case "number":
		return openapi3.TypeNumber
	case "integer":
		return openapi3.TypeInteger
	case "object":
		return openapi3.TypeObject
	case "array":
		return openapi3.TypeArray
	case "boolean":
		return openapi3.TypeBoolean
	default:
		return openapi3.TypeString
	}
}

