package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type journal struct {
	SchemaVersion int    `json:"schema_version"`
	InstanceID    string `json:"instance_id"`
	Kind          string `json:"kind"`
	Phase         string `json:"phase"`
	Backup        string `json:"backup"`
	HadContainer  bool   `json:"had_container"`
	OldRunning    bool   `json:"old_running"`
}

func (m *Manager) withLock(ctx context.Context, work func() error) error {
	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.breakOp()
	return work()
}
func (m *Manager) acquireLock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.rootCheck(); err != nil {
		return nil, err
	}
	if err := privateDir(filepath.Dir(m.paths.Lock())); err != nil {
		return nil, err
	}
	if err := safePath(m.paths.Lock()); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.paths.Lock(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func (m *Manager) begin(kind string, in *inspection) (result journal, beginErr error) {
	if _, err := os.Stat(m.paths.Journal()); err == nil {
		return journal{}, fmt.Errorf("存在未完成事务，请先恢复")
	} else if !os.IsNotExist(err) {
		return journal{}, err
	}
	id, err := randomHex(6)
	if err != nil {
		return journal{}, err
	}
	dir := filepath.Join(m.paths.Backups(), m.now().UTC().Format("20060102T150405.000000000Z")+"-"+id)
	if err = privateDir(dir); err != nil {
		return journal{}, err
	}
	defer func() {
		if beginErr != nil {
			// A failed directory sync may still have published the journal.
			if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
				beginErr = errors.Join(beginErr, os.RemoveAll(dir))
			}
		}
	}()
	var snapshots []fileSnapshot
	for _, p := range m.managedFiles() {
		s, err := snapshot(p)
		if err != nil {
			return journal{}, err
		}
		snapshots = append(snapshots, s)
	}
	if err = saveJSON(filepath.Join(dir, "snapshot.json"), snapshots); err != nil {
		return journal{}, err
	}
	j := journal{SchemaVersion: 1, InstanceID: Instance, Kind: kind, Phase: "prepared", Backup: dir}
	if in != nil {
		j.HadContainer = true
		j.OldRunning = in.State.Running
	}
	if err = saveJSON(m.paths.Journal(), j); err != nil {
		return j, err
	}
	m.logBackup(dir, snapshots)
	return j, nil
}
func (m *Manager) phase(j *journal, phase string) error {
	j.Phase = phase
	return saveJSON(m.paths.Journal(), j)
}
func (m *Manager) commit(j journal) error {
	if err := m.phase(&j, "committed"); err != nil {
		return err
	}
	if err := removePrivateFile(m.paths.Journal()); err != nil {
		return err
	}
	m.maintainFiles()
	return nil
}
func (m *Manager) rollback(j journal, cause error) error {
	m.op(phaseResult, levelError, "操作失败", "恢复旧配置、状态与容器")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := m.restoreTransaction(ctx, j)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("回滚未完成，保留恢复记录: %w", err))
	}
	return fmt.Errorf("%w；已恢复旧配置与运行状态", cause)
}
func (m *Manager) restoreTransaction(ctx context.Context, j journal) error {
	if j.SchemaVersion != 1 || j.InstanceID != Instance || filepath.Dir(j.Backup) != m.paths.Backups() {
		return fmt.Errorf("事务恢复记录标识/路径无效")
	}
	var snaps []fileSnapshot
	if err := loadSnapshot(filepath.Join(j.Backup, "snapshot.json"), &snaps); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, p := range m.managedFiles() {
		allowed[p] = true
	}
	if len(snaps) != len(allowed) {
		return fmt.Errorf("事务快照文件集合无效")
	}
	for _, s := range snaps {
		if !allowed[s.Path] {
			return fmt.Errorf("快照引用非受管路径")
		}
		delete(allowed, s.Path)
	}
	m.op(phaseBackup, levelInfo, "读取快照", filepath.Join(j.Backup, "snapshot.json"))
	// The on-disk Compose may already describe the new network while the
	// container still uses the old one. Verify ownership before restoring it.
	in, err := m.inspectWithCheck(ctx, m.checkOwnedIdentity)
	if err != nil {
		return err
	}
	if in != nil {
		if _, err = m.dockerOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "停止容器"}, "container", "stop", in.ID); err != nil {
			return err
		}
	}
	// Files are restored before runtime recreation. Metadata is consequently old.
	for _, s := range snaps {
		if err = restore(s); err != nil {
			return err
		}
	}
	m.op(phaseApply, levelStep, "恢复文件", "配置、状态和部署记录")
	if j.HadContainer {
		md, err := m.metadata()
		if err != nil {
			return err
		}
		if err = m.checkCompose(md); err != nil {
			return err
		}
		args := []string{"up", "--no-deps", "--pull", "never", "--force-recreate"}
		name := "重建容器"
		if j.OldRunning {
			args = append(args, "-d")
			name = "启动容器"
		} else {
			args = append(args, "--no-start")
		}
		args = append(args, Service)
		if _, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: name}, args...); err != nil {
			return err
		}
		current, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if current == nil || current.Image != md.ImageID || current.State.Running != j.OldRunning {
			return fmt.Errorf("恢复后容器状态或镜像异常")
		}
		if j.OldRunning {
			s, err := m.LoadState()
			if err != nil {
				return err
			}
			ports := hostPorts(s.Node, md.Network)
			m.op(phaseApply, levelInfo, "稳定检查", healthStartDetail(ports))
			if err = m.healthCheck(ctx, ports, md.ImageID); err != nil {
				return err
			}
			m.op(phaseApply, levelResult, "运行状态", "运行中，监听端口正常")
		}
	} else if in != nil {
		if _, err = m.dockerOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "删除容器"}, "container", "rm", in.ID); err != nil {
			return err
		}
	}
	if !j.HadContainer {
		if err = m.removeManagedNetwork(ctx); err != nil {
			return err
		}
	}
	if err = removePrivateFile(m.paths.Journal()); err != nil {
		return err
	}
	m.op(phaseApply, levelStep, "清除记录", m.paths.Journal())
	m.maintainFiles()
	m.op(phaseResult, levelResult, "恢复结果", "事务恢复完成")
	return nil
}
func loadSnapshot(path string, v any) error {
	// Snapshots contain multiple configuration files, so their bound is larger.
	const limit = 64 << 20
	f, err := openPrivateFile(path, limit)
	if err != nil {
		return err
	}
	defer f.Close()
	// Decode directly from the verified descriptor instead of also retaining
	// the whole encoded snapshot. The extra byte detects concurrent growth.
	r := &io.LimitedReader{R: f, N: limit + 1}
	err = decodeStrictReader(r, v)
	if r.N == 0 {
		return fmt.Errorf("文件超过大小限制: %s", path)
	}
	return err
}
func (m *Manager) recoverLocked(ctx context.Context) error {
	if pending, err := m.cleanupPending(); err != nil {
		return err
	} else if pending {
		return fmt.Errorf("数据清理尚未完成，请执行 proxydock cleanup --yes")
	}
	var j journal
	err := loadJSON(m.paths.Journal(), &j)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Phase == "committed" {
		if err = removePrivateFile(m.paths.Journal()); err != nil {
			return err
		}
		m.op(phaseApply, levelStep, "清除记录", m.paths.Journal())
		m.maintainFiles()
		m.op(phaseResult, levelResult, "恢复结果", "恢复记录已清除")
		return nil
	}
	m.op(phaseCheck, levelWarn, "发现事务", "未完成的"+transactionKindLabel(j.Kind))
	if err = m.CheckReady(ctx); err != nil {
		return err
	}
	return m.restoreTransaction(ctx, j)
}
func transactionKindLabel(kind string) string {
	switch kind {
	case "config":
		return "配置"
	case "upgrade":
		return "升级"
	case "prepare":
		return "准备镜像"
	default:
		if kind == "" {
			return ""
		}
		return kind
	}
}
func (m *Manager) Recover(ctx context.Context) error {
	return m.withLock(ctx, func() error {
		_, statErr := os.Stat(m.paths.Journal())
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		if _, err := m.cleanupRecordTemps(); err != nil {
			return err
		}
		if os.IsNotExist(statErr) {
			m.maintainFiles()
		}
		if _, err := m.lookPath("docker"); err == nil {
			if err = m.checkDocker(); err != nil {
				return err
			}
			if err = m.reapValidators(ctx); err != nil {
				return err
			}
		}
		if os.IsNotExist(statErr) {
			m.op(phaseResult, levelResult, "恢复结果", "没有未完成事务")
		}
		return nil
	})
}
