package runtime

import (
	"flag"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const DefaultAddress = "127.0.0.1:19463"

type Config struct {
	Address   string
	DataDir   string
	SelfCheck bool
}

func Parse(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("arcproof", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	addr := fs.String("addr", DefaultAddress, "HTTP 监听地址")
	dataDir := fs.String("data-dir", "./var/data", "数据目录")
	selfCheck := fs.Bool("self-check", false, "运行自检后退出")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("存在未知位置参数: %s", strings.Join(fs.Args(), " "))
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			explicit = true
		}
	})
	resolved := *addr
	if !explicit {
		if raw := strings.TrimSpace(getenv("PORT")); raw != "" {
			port, err := validatePort(raw)
			if err != nil {
				return Config{}, fmt.Errorf("PORT 无效: %w", err)
			}
			resolved = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		}
	}
	if err := ValidateAddress(resolved); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(*dataDir) == "" {
		return Config{}, fmt.Errorf("-data-dir 不能为空")
	}
	return Config{Address: resolved, DataDir: *dataDir, SelfCheck: *selfCheck}, nil
}
func ValidateAddress(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("监听地址格式无效: %w", err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("监听地址必须使用回环主机")
	}
	if _, err = validatePort(rawPort); err != nil {
		return fmt.Errorf("监听端口无效: %w", err)
	}
	return nil
}
func validatePort(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("端口不能为空")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("必须是十进制整数")
		}
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1024 || port > 65535 {
		return 0, fmt.Errorf("必须在 1024 至 65535 之间")
	}
	if port == 3000 || port == 8080 {
		return 0, fmt.Errorf("端口 %d 被禁止", port)
	}
	return port, nil
}
