package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"arcproof/internal/app"
	"arcproof/internal/clock"
	"arcproof/internal/httpapi"
	"arcproof/internal/store"
)

type summary struct {
	Status   string   `json:"status"`
	Total    int      `json:"total"`
	Imported int      `json:"imported"`
	DryRun   bool     `json:"dry_run"`
	BatchKey string   `json:"batch_key"`
	IDs      []string `json:"ids,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "导入失败:", err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arcproof-import", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "./var/data", "数据目录")
	file := fs.String("file", "", "NDJSON 文件")
	actor := fs.String("actor", "", "行为人")
	batchKey := fs.String("batch-key", "", "批次幂等键")
	dryRun := fs.Bool("dry-run", false, "仅校验")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*file) == "" || strings.TrimSpace(*actor) == "" {
		return fmt.Errorf("-file 和 -actor 必填")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var inputs []app.CreateRequirementInput
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var in app.CreateRequirementInput
		if err = httpapi.DecodeStrict(raw, &in); err != nil {
			return fmt.Errorf("第 %d 行: %w", line, err)
		}
		inputs = append(inputs, in)
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	repo, err := store.Open(*dataDir)
	if err != nil {
		return err
	}
	defer repo.Close()
	items, err := app.New(repo, clock.Real{}).ImportRequirements(strings.TrimSpace(*actor), strings.TrimSpace(*batchKey), inputs, *dryRun)
	if err != nil {
		return err
	}
	result := summary{Status: "ok", Total: len(inputs), Imported: len(items), DryRun: *dryRun, BatchKey: *batchKey}
	for _, item := range items {
		result.IDs = append(result.IDs, item.ID)
	}
	fmt.Fprintf(out, "校验通过：共 %d 条，落盘 %d 条。\n", result.Total, map[bool]int{true: 0, false: result.Imported}[*dryRun])
	return json.NewEncoder(out).Encode(result)
}
