package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultScriptURL = "https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh"
const maxScriptSize = 2 << 20

var defaultHosts = []string{
	"github.com", "raw.githubusercontent.com",
	"release-assets.githubusercontent.com", "objects.githubusercontent.com",
}

type downloadedScript struct {
	content  []byte
	finalURL string
	sha256   string
}

func (u Updater) downloadScript(ctx context.Context, scriptURL string) (downloadedScript, error) {
	hosts := u.AllowedHosts
	if len(hosts) == 0 {
		hosts = defaultHosts
	}
	parsed, err := url.Parse(scriptURL)
	if err != nil {
		return downloadedScript{}, err
	}
	if err := checkScriptURL(parsed, hosts); err != nil {
		return downloadedScript{}, err
	}
	var client http.Client
	if u.Client != nil {
		client = *u.Client
	}
	if client.Timeout <= 0 {
		client.Timeout = 45 * time.Second
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("脚本重定向次数过多")
		}
		return checkScriptURL(req.URL, hosts)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return downloadedScript{}, err
	}
	req.Header.Set("Accept", "text/x-shellscript,text/plain;q=0.9,*/*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return downloadedScript{}, fmt.Errorf("下载安装脚本: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return downloadedScript{}, fmt.Errorf("安装脚本 HTTP 状态 %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScriptSize+1))
	if err != nil {
		return downloadedScript{}, fmt.Errorf("读取安装脚本: %w", err)
	}
	if len(body) > maxScriptSize {
		return downloadedScript{}, fmt.Errorf("安装脚本超过 %d bytes", maxScriptSize)
	}
	if err := validateScript(body); err != nil {
		return downloadedScript{}, err
	}
	return downloadedScript{
		content: body, finalURL: resp.Request.URL.String(),
		sha256: fmt.Sprintf("%x", sha256.Sum256(body)),
	}, nil
}

func checkScriptURL(u *url.URL, hosts []string) error {
	if u.Scheme != "https" {
		return fmt.Errorf("安装脚本只允许 HTTPS")
	}
	for _, allowed := range hosts {
		if strings.EqualFold(u.Hostname(), allowed) {
			return nil
		}
	}
	return fmt.Errorf("安装脚本地址主机 %q 不在白名单", u.Hostname())
}

func validateScript(body []byte) error {
	if len(body) == 0 {
		return fmt.Errorf("安装脚本为空")
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return fmt.Errorf("安装脚本包含 NUL 字节")
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("安装脚本不是有效 UTF-8 文本")
	}
	first, _, multiline := bytes.Cut(body, []byte("\n"))
	if !multiline {
		return fmt.Errorf("安装脚本不是有效文本脚本")
	}
	if !bytes.HasPrefix(first, []byte("#!")) {
		return fmt.Errorf("安装脚本缺少受支持的 sh/bash shebang")
	}
	fields := strings.Fields(string(first[2:]))
	validShell := func(name string) bool { return name == "bash" || name == "sh" }
	valid := len(fields) > 0 && validShell(filepath.Base(fields[0]))
	if !valid && len(fields) >= 2 && filepath.Base(fields[0]) == "env" {
		valid = validShell(fields[1])
	}
	if !valid {
		return fmt.Errorf("安装脚本缺少受支持的 sh/bash shebang")
	}
	controls := 0
	for _, value := range body {
		if value < 0x09 || (value > 0x0d && value < 0x20) {
			controls++
		}
	}
	if controls > len(body)/100 {
		return fmt.Errorf("安装脚本包含过多非文本控制字符")
	}
	return nil
}
