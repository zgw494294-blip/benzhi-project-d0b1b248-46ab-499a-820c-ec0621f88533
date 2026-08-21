package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"arcproof/internal/app"
	"arcproof/internal/clock"
	"arcproof/internal/httpapi"
	"arcproof/internal/jobs"
	"arcproof/internal/runtime"
	"arcproof/internal/sample"
	"arcproof/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	cfg, err := runtime.Parse(args, os.Getenv)
	if err != nil {
		return err
	}
	if cfg.SelfCheck {
		return selfCheck(cfg)
	}
	repo, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer repo.Close()
	service := app.New(repo, clock.Real{})
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	manager := jobs.New(service, logger)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", cfg.Address, err)
	}
	server := newHTTPServer(httpapi.New(service, logger).Handler())
	logger.Info("弧证服务已启动", "address", cfg.Address, "data_dir", cfg.DataDir)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err = server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("关闭 HTTP 服务: %w", err)
		}
		return nil
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}

func selfCheck(cfg runtime.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(cfg.DataDir, "arcproof-self-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	repo, err := store.Open(dir)
	if err != nil {
		return err
	}
	service := app.New(repo, clock.Real{})
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("自检监听 %s: %w", cfg.Address, err)
	}
	server := newHTTPServer(httpapi.New(service, slog.New(slog.NewTextHandler(ioDiscard{}, nil))).Handler())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	client := &http.Client{Timeout: 3 * time.Second}
	healthURL := "http://" + listener.Addr().String() + "/api/v1/healthz"
	response, err := client.Get(healthURL)
	if err != nil {
		return fmt.Errorf("自检健康请求: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("自检健康接口返回 %d", response.StatusCode)
	}
	rule, err := service.CreateRuleSet("self-check", sample.Rule())
	if err != nil {
		return err
	}
	rule, err = service.PublishRuleSet("self-check", rule.ID, rule.Version)
	if err != nil {
		return err
	}
	q, err := service.CreateQualification("self-check", app.CreateQualificationInput{RuleSetID: rule.ID, Variables: sample.Variables()})
	if err != nil {
		return err
	}
	for i, e := range sample.Evidence(q.Version) {
		_, err = service.AddEvidence("self-check", q.ID, fmt.Sprintf("self-%d", i), e)
		if err != nil {
			return err
		}
	}
	q, err = service.GetQualification(q.ID)
	if err != nil {
		return err
	}
	q, err = service.EvaluateQualification("self-check", q.ID, q.Version)
	if err != nil {
		return err
	}
	pr, err := service.CreateProcedure("self-check", app.CreateProcedureInput{Name: "WPS-SC", RuleSetID: rule.ID, QualificationIDs: []string{q.ID}, Variables: sample.Variables()})
	if err != nil {
		return err
	}
	rev, err := service.DeriveCoverage("self-check", pr.Revision.ID, pr.Revision.Version)
	if err != nil {
		return err
	}
	rev, err = service.PublishRevision("self-check", rev.ID, rev.Version)
	if err != nil {
		return err
	}
	joint, err := service.CreateRequirement("self-check", "joint-self-check", app.CreateRequirementInput{Reference: "JOINT-SC", Variables: sample.Variables()})
	if err != nil {
		return err
	}
	assessment, err := service.AssessRequirement("self-check", joint.ID, app.CreateAssessmentInput{RevisionID: rev.ID, ExpectedRequirementVersion: joint.Version})
	if err != nil {
		return err
	}
	if assessment.Conclusion != "APPLICABLE" {
		return fmt.Errorf("适用性结论异常: %s", assessment.Conclusion)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	<-done
	if err = repo.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("持久化重放失败: %w", err)
	}
	defer reopened.Close()
	if app.New(reopened, clock.Real{}).Health().Status != "ok" {
		return fmt.Errorf("重启后健康检查失败")
	}
	result := map[string]any{"status": "ok", "address": cfg.Address, "rule_set_id": rule.ID, "qualification_id": q.ID, "revision_id": rev.ID, "assessment_id": assessment.ID, "persistence": "replayed", "audit": "valid"}
	return json.NewEncoder(os.Stdout).Encode(result)
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
