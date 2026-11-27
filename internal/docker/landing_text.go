package docker

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"proxydock/internal/docker/model"
)

const maxLandingTextSize = 64 << 10

// LandingBundle adds an explicit identity and version to portable credentials.
type LandingBundle struct {
	ManagedBy     string            `json:"managed_by"`
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	Peer          model.LandingPeer `json:"peer"`
}

func landingText(peer model.LandingPeer) ([]byte, error) {
	return jsonBytes(LandingBundle{ManagedBy: "proxydock", SchemaVersion: 1, Kind: "proxydock-landing", Peer: peer})
}

// ParseLandingText accepts existing bare peers and versioned ProxyDock or
// ProxyForge bundles. Only the client-side peer fields can enter managed state.
func ParseLandingText(data []byte) (model.LandingPeer, error) {
	var peer model.LandingPeer
	if len(data) == 0 || len(data) > maxLandingTextSize {
		return peer, fmt.Errorf("落地连接文本不能为空且不能超过 64 KiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return peer, fmt.Errorf("落地连接文本必须是有效 JSON 对象")
	}
	_, bundled := fields["peer"]
	_, identified := fields["managed_by"]
	if bundled || identified {
		var bundle LandingBundle
		if err := decodeStrict(data, &bundle); err != nil {
			return peer, fmt.Errorf("解析落地连接文本: %w", err)
		}
		valid := bundle.ManagedBy == "proxydock" && bundle.Kind == "proxydock-landing" && bundle.SchemaVersion == 1
		valid = valid || bundle.ManagedBy == "proxyforge" && bundle.Kind == "proxyforge-landing" && bundle.SchemaVersion >= 1 && bundle.SchemaVersion <= 3
		if !valid {
			return peer, fmt.Errorf("落地连接文本标识或版本无效")
		}
		peer = bundle.Peer
		if peer.Security == "" && bundle.ManagedBy == "proxyforge" {
			peer.Security = model.LandingSecurityReality
		}
	} else if err := decodeStrict(data, &peer); err != nil {
		return peer, fmt.Errorf("解析落地连接文本: %w", err)
	}
	peer.Security = model.NormalizeLandingSecurity(peer.Security)
	if err := validatePeer(peer); err != nil {
		return model.LandingPeer{}, err
	}
	return peer, nil
}

func readLandingInput(path string, fromStdin bool, input io.Reader) (model.LandingPeer, error) {
	path = strings.TrimSpace(path)
	if path != "" && fromStdin {
		return model.LandingPeer{}, fmt.Errorf("文件导入和 --upstream-stdin 不能同时使用")
	}
	var data []byte
	var err error
	if fromStdin {
		if input == nil {
			return model.LandingPeer{}, fmt.Errorf("标准输入不可用")
		}
		data, err = io.ReadAll(io.LimitReader(input, maxLandingTextSize+1))
	} else if path != "" {
		path, err = filepath.Abs(path)
		if err == nil {
			data, err = readPrivateLimit(path, maxLandingTextSize)
		}
	} else {
		return model.LandingPeer{}, fmt.Errorf("需要 --upstream-stdin 或 --peer-file（也支持 --upstream）")
	}
	if err != nil {
		return model.LandingPeer{}, fmt.Errorf("读取落地连接文本: %w", err)
	}
	return ParseLandingText(data)
}
