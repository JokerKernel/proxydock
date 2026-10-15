package docker

import (
	"context"
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
var validationDirectoryPattern = regexp.MustCompile(`^validation-[0-9]+$`)
var temporaryNamePatterns = map[string]*regexp.Regexp{
	".docker-write-": regexp.MustCompile("^\\.docker-write-[a-f0-9]{24}$"),
	"candidate-":     regexp.MustCompile("^candidate-[0-9]+\\.json$"),
	"edit-":          regexp.MustCompile("^edit-[0-9]+\\.json$"),
	".edit-":         regexp.MustCompile("^\\.edit-[0-9]+\\.json\\.sw[opx]$"),
}

func (m *Manager) cleanupPending() (bool, error) {
	var record ownership
	if err := loadJSON(m.paths.CleanupRecord(), &record); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if record.SchemaVersion != 1 || record.ManagedBy != "proxydock" || record.InstanceID != Instance {
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
	if err := m.cleanupTransactionSnapshots(); err != nil {
		return err
	}
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
	if err := m.cleanupValidationDirs(); err != nil {
		return err
	}
	backupRefs, err := m.pruneBackups()
	if err != nil {
		return err
	}
	for name := range backupRefs {
		refs[name] = true
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

func (m *Manager) cleanupValidationDirs() error {
	entries, err := os.ReadDir(m.paths.Staging())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validationDirectoryPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(m.paths.Staging(), entry.Name())
		var marker ownership
		if err := loadJSON(filepath.Join(dir, ".proxydock-validation.json"), &marker); err != nil || marker.SchemaVersion != 1 || marker.ManagedBy != "proxydock" || marker.InstanceID != Instance {
			continue
		}
		if err := safePath(dir); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func removeTemporaryFiles(dir string, prefixes ...string) error {
	if err := safePath(dir); err != nil {
		return err
	}
	opened, err := openSecureDir(dir, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer opened.Close()
	entries, err := opened.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		matched := false
		for _, prefix := range prefixes {
			pattern := temporaryNamePatterns[prefix]
			matched = matched || pattern != nil && pattern.MatchString(entry.Name())
		}
		if !matched {
			continue
		}
		f, err := opened.openFile(entry.Name(), os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		info, statErr := f.Stat()
		f.Close()
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			continue
		}
		if err := opened.unlink(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

// Keep three generated configuration backups. Read only config.json; imported
// configuration backups are retained and also protect certificate references.
func (m *Manager) pruneBackups() (map[string]bool, error) {
	if err := safePath(m.paths.Backups()); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.paths.Backups())
	if os.IsNotExist(err) {
		entries = nil
	} else if err != nil {
		return nil, err
	}
	type record struct {
		name      string
		generated bool
		refs      map[string]bool
	}
	var records []record
	var generated []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.paths.Backups(), entry.Name())
		item := record{name: entry.Name(), generated: backupNamePattern.MatchString(entry.Name()), refs: map[string]bool{}}
		if item.generated {
			if err := removeTemporaryFiles(dir, ".docker-write-"); err != nil {
				return nil, err
			}
		}
		data, err := readPrivate(filepath.Join(dir, "config.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("备份配置损坏，保留证书: %s", dir)
		}
		if err := m.certificateReferences(data, item.refs); err != nil {
			return nil, err
		}
		records = append(records, item)
		if item.generated {
			generated = append(generated, item.name)
		}
	}
	// First deployment has no previous config. Save its completed config once.
	if len(generated) == 0 {
		data, err := readPrivate(m.paths.Config())
		if err == nil {
			refs := map[string]bool{}
			if err := m.certificateReferences(data, refs); err != nil {
				return nil, err
			}
			id, err := randomHex(6)
			if err != nil {
				return nil, err
			}
			name := m.now().UTC().Format("20060102T150405.000000000Z") + "-" + id
			if err := m.saveConfigBackup(name, data); err != nil {
				return nil, err
			}
			records = append(records, record{name: name, generated: true, refs: refs})
			generated = append(generated, name)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(generated)
	keep := map[string]bool{}
	for _, name := range generated[max(0, len(generated)-3):] {
		keep[name] = true
	}
	refs := map[string]bool{}
	// Validate all records before deleting any backup.
	for _, item := range records {
		if item.generated && !keep[item.name] {
			dir := filepath.Join(m.paths.Backups(), item.name)
			if err := safePath(dir); err != nil {
				return nil, err
			}
			if err := os.RemoveAll(dir); err != nil {
				return nil, err
			}
			continue
		}
		for name := range item.refs {
			refs[name] = true
		}
	}
	return refs, nil
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
	marked := markerErr == nil && marker.SchemaVersion == 1 && marker.ManagedBy == "proxydock" && marker.InstanceID == Instance
	if !marked {
		return false
	}
	for _, entry := range entries {
		if (entry.Name() != "cert.pem" && entry.Name() != "key.pem" && entry.Name() != ".proxydock-certificate.json" && !temporaryNamePatterns[".docker-write-"].MatchString(entry.Name())) || !entry.Type().IsRegular() {
			return false
		}
		if _, err := readPrivate(filepath.Join(dir, entry.Name())); err != nil {
			return false
		}
	}
	return true
}

func (m *Manager) reapValidators(ctx context.Context) error {
	b, err := m.docker(ctx, "container", "ls", "--all", "--no-trunc", "--filter", "label=io.proxydock.managed=true", "--filter", "label=io.proxydock.instance="+Instance, "--filter", "label=io.proxydock.role=validator", "--format", "{{.ID}}")
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
		if in.ID != id || !validatorNamePattern.MatchString(in.Name) || labels["io.proxydock.managed"] != "true" || labels["io.proxydock.instance"] != Instance || labels["io.proxydock.role"] != "validator" {
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
