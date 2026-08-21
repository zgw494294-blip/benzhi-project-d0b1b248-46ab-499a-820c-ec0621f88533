package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arcproof/internal/domain"
)

const snapshotName = "state.json"
const journalName = "journal.ndjson"

type JournalEntry struct {
	Sequence       uint64       `json:"sequence"`
	PreviousDigest string       `json:"previous_digest"`
	State          domain.State `json:"state"`
	Digest         string       `json:"digest"`
}

type Repository struct {
	mu             sync.RWMutex
	dir            string
	state          domain.State
	degraded       bool
	degradedReason string
	closed         bool
}

func Open(dir string) (*Repository, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("数据目录不能为空")
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	r := &Repository{dir: dir, state: domain.NewState()}
	if err := r.loadSnapshot(); err != nil {
		return nil, err
	}
	if err := r.replayJournal(); err != nil {
		return nil, err
	}
	if err := ValidateState(r.state); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repository) Directory() string { return r.dir }
func (r *Repository) Degraded() (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.degraded, r.degradedReason
}

// Probe 验证数据目录当前仍可创建、同步并清理文件。
func (r *Repository) Probe() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return errors.New("仓储已关闭")
	}
	f, err := os.CreateTemp(r.dir, ".health-probe-*")
	if err != nil {
		return fmt.Errorf("创建健康探测文件: %w", err)
	}
	name := f.Name()
	if _, err = f.Write([]byte("arcproof-health\n")); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	removeErr := os.Remove(name)
	if err != nil {
		return fmt.Errorf("写入健康探测文件: %w", err)
	}
	if removeErr != nil {
		return fmt.Errorf("清理健康探测文件: %w", removeErr)
	}
	return nil
}
func (r *Repository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.writeSnapshot(r.state)
}

func (r *Repository) View(fn func(domain.State) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return errors.New("仓储已关闭")
	}
	copy, err := cloneState(r.state)
	if err != nil {
		return err
	}
	return fn(copy)
}

func (r *Repository) Snapshot() (domain.State, error) {
	var result domain.State
	err := r.View(func(s domain.State) error { result = s; return nil })
	return result, err
}

func (r *Repository) Update(actor, action, objectType, objectID string, fn func(*domain.State) error) error {
	if strings.TrimSpace(actor) == "" {
		return domain.Invalid("行为人不能为空", nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("仓储已关闭")
	}
	next, err := cloneState(r.state)
	if err != nil {
		return err
	}
	before := stateContentDigest(next)
	beforeContent := stateContentDigestWithoutAudit(next)
	if err = fn(&next); err != nil {
		return err
	}
	// 幂等重放等没有改变领域状态的事务不应制造新的序号和审计事件。
	// 这也避免重复请求在审计链中伪造一次新的业务动作。
	if stateContentDigestWithoutAudit(next) == beforeContent {
		return nil
	}
	next.Sequence = r.state.Sequence + 1
	after := stateContentDigestWithoutAudit(next)
	prevHash := ""
	if len(next.Audit) > 0 {
		prevHash = next.Audit[len(next.Audit)-1].Hash
	}
	event := domain.AuditEvent{Sequence: next.Sequence, Time: time.Now().UTC(), Actor: strings.TrimSpace(actor), Action: action, ObjectType: objectType, ObjectID: objectID, BeforeDigest: before, AfterDigest: after, PreviousHash: prevHash}
	event.Hash = auditHash(event)
	next.Audit = append(next.Audit, event)
	next.Digest = stateDigest(next)
	entry := JournalEntry{Sequence: next.Sequence, PreviousDigest: r.state.Digest, State: next}
	entry.Digest = journalDigest(entry)
	if err = r.appendJournal(entry); err != nil {
		return err
	}
	if err = r.writeSnapshot(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

func (r *Repository) Compact() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("仓储已关闭")
	}
	if err := r.writeSnapshot(r.state); err != nil {
		return err
	}
	journal := filepath.Join(r.dir, journalName)
	previous := journal + ".previous"
	_ = os.Remove(previous)
	if _, err := os.Stat(journal); err == nil {
		if err = os.Rename(journal, previous); err != nil {
			return fmt.Errorf("轮换事务日志: %w", err)
		}
	}
	f, err := os.OpenFile(journal, os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	return f.Close()
}

func (r *Repository) loadSnapshot() error {
	path := filepath.Join(r.dir, snapshotName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取快照: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var s domain.State
	if err = dec.Decode(&s); err != nil {
		return fmt.Errorf("解析快照: %w", err)
	}
	if err = ensureEOF(dec); err != nil {
		return fmt.Errorf("解析快照: %w", err)
	}
	if s.SchemaVersion != domain.SchemaVersion {
		return fmt.Errorf("不支持的快照版本 %d", s.SchemaVersion)
	}
	if s.Digest == "" || s.Digest != stateDigest(s) {
		return fmt.Errorf("快照摘要校验失败")
	}
	r.state = s
	return nil
}

func (r *Repository) replayJournal() error {
	path := filepath.Join(r.dir, journalName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return fmt.Errorf("打开事务日志: %w", err)
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	offset := int64(0)
	lineNo := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		lineNo++
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				r.degraded = true
				r.degradedReason = "已截断事务日志末尾的不完整记录"
				if err = f.Truncate(offset); err != nil {
					return fmt.Errorf("截断日志: %w", err)
				}
				break
			}
			var entry JournalEntry
			if err = json.Unmarshal(bytes.TrimSpace(line), &entry); err != nil {
				return fmt.Errorf("事务日志第 %d 行损坏: %w", lineNo, err)
			}
			if entry.Digest != journalDigest(entry) {
				return fmt.Errorf("事务日志第 %d 行摘要错误", lineNo)
			}
			if entry.Sequence > r.state.Sequence {
				if entry.Sequence != r.state.Sequence+1 {
					return fmt.Errorf("事务日志序号跳跃: 当前 %d，收到 %d", r.state.Sequence, entry.Sequence)
				}
				if entry.PreviousDigest != r.state.Digest {
					return fmt.Errorf("事务日志前序摘要不匹配")
				}
				if entry.State.Sequence != entry.Sequence || entry.State.Digest != stateDigest(entry.State) {
					return fmt.Errorf("事务日志状态摘要错误")
				}
				r.state = entry.State
			}
			offset += int64(len(line))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("读取事务日志: %w", readErr)
		}
	}
	return nil
}

func (r *Repository) appendJournal(entry JournalEntry) error {
	f, err := os.OpenFile(filepath.Join(r.dir, journalName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("打开事务日志: %w", err)
	}
	b, err := json.Marshal(entry)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("写事务日志: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func (r *Repository) writeSnapshot(state domain.State) error {
	state.Digest = stateDigest(state)
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(r.dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时快照: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0640); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写快照: %w", err)
	}
	var verify domain.State
	raw, err := os.ReadFile(tmpName)
	if err == nil {
		err = json.Unmarshal(raw, &verify)
	}
	if err != nil || verify.Digest != stateDigest(verify) {
		return fmt.Errorf("临时快照回读校验失败")
	}
	if err = os.Rename(tmpName, filepath.Join(r.dir, snapshotName)); err != nil {
		return fmt.Errorf("替换快照: %w", err)
	}
	dir, err := os.Open(r.dir)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return fmt.Errorf("同步数据目录: %w", err)
	}
	ok = true
	return nil
}

func ValidateState(s domain.State) error {
	if s.SchemaVersion != domain.SchemaVersion {
		return fmt.Errorf("状态版本不兼容")
	}
	if s.Digest != "" && s.Digest != stateDigest(s) {
		return fmt.Errorf("状态摘要校验失败")
	}
	previous := ""
	var sequence uint64
	for i, event := range s.Audit {
		if event.Sequence <= sequence {
			return fmt.Errorf("审计事件 %d 序号不递增", i)
		}
		if event.PreviousHash != previous {
			return fmt.Errorf("审计事件 %d 前序哈希断裂", i)
		}
		if event.Hash != auditHash(event) {
			return fmt.Errorf("审计事件 %d 哈希错误", i)
		}
		previous = event.Hash
		sequence = event.Sequence
	}
	if len(s.Audit) > 0 && s.Audit[len(s.Audit)-1].Sequence > s.Sequence {
		return fmt.Errorf("审计序号超过状态序号")
	}
	return nil
}

func cloneState(s domain.State) (domain.State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return domain.State{}, err
	}
	var out domain.State
	err = json.Unmarshal(b, &out)
	return out, err
}
func stateDigest(s domain.State) string        { s.Digest = ""; return domain.Digest(s) }
func stateContentDigest(s domain.State) string { return stateDigest(s) }
func stateContentDigestWithoutAudit(s domain.State) string {
	s.Digest = ""
	s.Audit = nil
	return domain.Digest(s)
}
func auditHash(e domain.AuditEvent) string { e.Hash = ""; return domain.Digest(e) }
func journalDigest(e JournalEntry) string {
	e.Digest = ""
	h := sha256.Sum256(mustJSON(e))
	return hex.EncodeToString(h[:])
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	}
	return fmt.Errorf("存在多余 JSON 内容")
}
