package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var backupNamePattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[a-f0-9]{12}$`)
var validatorNamePattern = regexp.MustCompile(`^/?` + Container + `-check-[a-f0-9]{24}$`)
var certificateSNIPattern = regexp.MustCompile(`^pf-[a-f0-9]{24}\.invalid$`)

func (m *Manager) cleanupPending() (bool, error) {
	var record ownership
	if err := loadJSON(m.paths.CleanupRecord(), &record); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if record.SchemaVersion != 1 || record.ManagedBy != "proxyforge" || record.InstanceID != Instance {
		return false, fmt.Errorf("清理记录归属无效，拒绝继续删除")
	}
	return true, nil
}

// Called with the instance lock held, only outside an unfinished transaction.
// Maintenance errors must not turn a committed operation into a rollback.
func (m *Manager) maintainFiles() {
	if err := m.checkOwner(); err != nil {
		return
	}
	if err := m.checkNoTransaction(); err != nil {
		return
	}
	if pending, err := m.cleanupPending(); err != nil || pending {
		return
	}
	if err := m.maintainFilesLocked(); err != nil {
		m.op(phaseApply, levelWarn, "清理残留", err.Error())
	}
}

func (m *Manager) maintainFilesLocked() error {
	refs := map[string]bool{}
	for _, path := range []string{m.paths.State(), m.paths.Config()} {
		data, err := readPrivate(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err = m.certificateReferences(data, refs); err != nil {
			return err
		}
	}
	for _, dir := range []string{m.paths.Data(), m.paths.Deploy(), filepath.Dir(m.paths.Config()), m.paths.TLS()} {
		if err := removeTemporaryFiles(dir, ".docker-write-"); err != nil {
			return err
		}
	}
	if err := removeTemporaryFiles(m.paths.Staging(), "candidate-", "edit-", ".edit-", ".docker-write-"); err != nil {
		return err
	}
	backups, err := m.pruneBackups()
	if err != nil {
		return err
	}
	for _, snapshots := range backups {
		for _, snap := range snapshots {
			if snap.Exists && (snap.Path == m.paths.State() || snap.Path == m.paths.Config()) {
				if err := m.certificateReferences(snap.Data, refs); err != nil {
					return err
				}
			}
		}
	}
	entries, err := os.ReadDir(m.paths.TLS())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.paths.TLS(), entry.Name())
		if !managedCertificateDirectory(dir) {
			continue
		}
		if err := removeTemporaryFiles(dir, ".docker-write-"); err != nil {
			return err
		}
		if refs[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		m.op(phaseApply, levelStep, "回收证书", dir)
	}
	return nil
}

func removeTemporaryFiles(dir string, prefixes ...string) error {
	if err := safePath(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		matched := false
		for _, prefix := range prefixes {
			matched = matched || strings.HasPrefix(entry.Name(), prefix)
		}
		if !matched {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, err := readPrivate(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// Only generated backup directories are pruned; imported backups still protect
// their certificate references. Invalid snapshots stop certificate collection.
func (m *Manager) pruneBackups() ([][]fileSnapshot, error) {
	if err := safePath(m.paths.Backups()); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.paths.Backups())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	known := map[string][]fileSnapshot{}
	var generated []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.paths.Backups(), entry.Name())
		isGenerated := backupNamePattern.MatchString(entry.Name())
		if isGenerated {
			if err := removeTemporaryFiles(dir, ".docker-write-"); err != nil {
				return nil, err
			}
		}
		var snapshots []fileSnapshot
		err := loadSnapshot(filepath.Join(dir, "snapshot.json"), &snapshots)
		if os.IsNotExist(err) {
			contents, readErr := os.ReadDir(dir)
			if readErr != nil {
				return nil, readErr
			}
			if isGenerated && len(contents) == 0 {
				if err := os.Remove(dir); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		allowed := map[string]bool{}
		for _, path := range m.managedFiles() {
			allowed[path] = true
		}
		if len(snapshots) != len(allowed) {
			return nil, fmt.Errorf("备份快照文件集合无效: %s", dir)
		}
		for _, snap := range snapshots {
			if !allowed[snap.Path] {
				return nil, fmt.Errorf("备份引用非受管路径: %s", dir)
			}
			delete(allowed, snap.Path)
		}
		// Validate JSON before pruning any backup or collecting certificates.
		for _, snap := range snapshots {
			if snap.Exists && (snap.Path == m.paths.State() || snap.Path == m.paths.Config()) && !json.Valid(snap.Data) {
				return nil, fmt.Errorf("备份配置损坏，保留证书: %s", dir)
			}
		}
		known[entry.Name()] = snapshots
		if isGenerated {
			generated = append(generated, entry.Name())
		}
	}
	sort.Strings(generated)
	for len(generated) > 3 {
		name := generated[0]
		dir := filepath.Join(m.paths.Backups(), name)
		if err := safePath(dir); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
		delete(known, name)
		generated = generated[1:]
	}
	var retained [][]fileSnapshot
	for _, snapshots := range known {
		retained = append(retained, snapshots)
	}
	return retained, nil
}

func (m *Manager) certificateReferences(data []byte, refs map[string]bool) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("配置无法解析，保留证书: %w", err)
	}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			value = filepath.Clean(value)
			for _, prefix := range []string{m.paths.TLS() + "/", "/etc/xray/tls/"} {
				if strings.HasPrefix(value, prefix) {
					rel := strings.TrimPrefix(value, prefix)
					refs[strings.Split(rel, "/")[0]] = true
				}
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(value)
	return nil
}

func managedCertificateDirectory(dir string) bool {
	if safePath(dir) != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var marker ownership
	markerErr := loadJSON(filepath.Join(dir, ".proxydock-certificate.json"), &marker)
	marked := markerErr == nil && marker.SchemaVersion == 1 && marker.ManagedBy == "proxyforge" && marker.InstanceID == Instance
	if markerErr != nil && !os.IsNotExist(markerErr) || markerErr == nil && !marked {
		return false
	}
	for _, entry := range entries {
		if (entry.Name() != "cert.pem" && entry.Name() != "key.pem" && entry.Name() != ".proxydock-certificate.json" && !strings.HasPrefix(entry.Name(), ".docker-write-")) || !entry.Type().IsRegular() {
			return false
		}
		if _, err := readPrivate(filepath.Join(dir, entry.Name())); err != nil {
			return false
		}
	}
	if marked {
		return true
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil || len(pair.Certificate) != 1 {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	return err == nil && len(leaf.Subject.Organization) == 1 && leaf.Subject.Organization[0] == "ProxyDock" && certificateSNIPattern.MatchString(leaf.Subject.CommonName) && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

func (m *Manager) reapValidators(ctx context.Context) error {
	b, err := m.docker(ctx, "container", "ls", "--all", "--no-trunc", "--filter", "label=io.proxyforge.managed=true", "--filter", "label=io.proxyforge.instance="+Instance, "--filter", "label=io.proxyforge.role=validator", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	for _, id := range stringsFields(b) {
		b, err := m.docker(ctx, "container", "inspect", id)
		if err != nil {
			// --rm may have removed a completed validator after the listing.
			remaining, queryErr := m.docker(ctx, "container", "ls", "--all", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
			if queryErr == nil && len(stringsFields(remaining)) == 0 {
				continue
			}
			return err
		}
		var values []inspection
		if json.Unmarshal(b, &values) != nil || len(values) != 1 {
			return fmt.Errorf("校验容器详情无效")
		}
		in := values[0]
		labels := in.Config.Labels
		if in.ID != id || !validatorNamePattern.MatchString(in.Name) || labels["io.proxyforge.managed"] != "true" || labels["io.proxyforge.instance"] != Instance || labels["io.proxyforge.role"] != "validator" {
			return fmt.Errorf("校验容器归属不匹配，拒绝移除: %s", id)
		}
		if _, err = m.dockerOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "清理校验容器"}, "container", "rm", "--force", "--volumes", id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) removeManagedNetwork(ctx context.Context) error {
	b, err := m.docker(ctx, "network", "ls", "--no-trunc", "--filter", "label=com.docker.compose.project="+Project, "--filter", "label=com.docker.compose.network=default", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	for _, id := range stringsFields(b) {
		b, err := m.docker(ctx, "network", "inspect", id)
		if err != nil {
			return err
		}
		var values []struct {
			ID         string
			Name       string
			Labels     map[string]string
			Containers map[string]json.RawMessage
		}
		if json.Unmarshal(b, &values) != nil || len(values) != 1 {
			return fmt.Errorf("网络详情无效")
		}
		in := values[0]
		if in.ID != id || in.Name != Project+"_default" || in.Labels["com.docker.compose.project"] != Project || in.Labels["com.docker.compose.network"] != "default" || len(in.Containers) != 0 {
			return fmt.Errorf("网络归属不匹配或仍有容器使用，拒绝删除: %s", id)
		}
		if _, err = m.dockerOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "移除实例网络"}, "network", "rm", id); err != nil {
			return err
		}
	}
	return nil
}
