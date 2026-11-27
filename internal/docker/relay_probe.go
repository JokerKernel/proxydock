package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"proxydock/internal/docker/model"
)

var ErrRelayUnreachable = errors.New("落地端点不可达")

type RelayOptions struct {
	AllowUnreachable bool
}

func probeRelayTCP(ctx context.Context, host string, port int) error {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

func validateRelayAddress(server string) error {
	if addr, err := netip.ParseAddr(server); err == nil {
		addr = addr.Unmap()
		lan := netip.MustParsePrefix("192.168.0.0/16")
		if !lan.Contains(addr) && (!addr.IsGlobalUnicast() || addr.IsPrivate() || forbiddenAddress(addr)) {
			return fmt.Errorf("落地地址必须是公网单播 IP、有效域名或 192.168.0.0/16 联调地址")
		}
	}
	return nil
}

func validateRelayEndpoint(n model.Node, peer model.LandingPeer) error {
	if err := validateRelayAddress(peer.Server); err != nil {
		return err
	}
	// Reject direct loops through any local managed ingress, including disabled
	// independent landings that can later be re-enabled.
	sameHost := strings.EqualFold(strings.TrimSuffix(peer.Server, "."), strings.TrimSuffix(n.Server, "."))
	localIP, localErr := netip.ParseAddr(n.Server)
	peerIP, peerErr := netip.ParseAddr(peer.Server)
	if localErr == nil && peerErr == nil {
		sameHost = localIP.Unmap() == peerIP.Unmap()
	}
	if sameHost {
		if peer.Port == n.Port || n.XrayFallbackGuard && peer.Port == n.XrayFallbackPort {
			return fmt.Errorf("落地端点不能指向本机受管监听地址和端口")
		}
		for _, access := range n.LandingAccesses {
			if access.Port != 0 && peer.Port == access.Port {
				return fmt.Errorf("落地端点不能指向本机落地接入")
			}
		}
	}
	return nil
}

func (m *Manager) checkRelay(ctx context.Context, peer model.LandingPeer, opts RelayOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.reachable(ctx, peer.Server, peer.Port); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !opts.AllowUnreachable {
			return fmt.Errorf("%w: %v；确认仍要保存时使用 --allow-unreachable", ErrRelayUnreachable, err)
		}
		m.op(phaseCheck, levelWarn, "连通性检查", "落地端点当前不可达，按要求继续保存："+err.Error())
	}
	return nil
}

func (m *Manager) TestRelay(ctx context.Context, name string) error {
	s, err := m.readManagedState(ctx)
	if err != nil {
		return err
	}
	for _, link := range s.Node.RelayLinks {
		if link.Name == name {
			if err := validateRelayEndpoint(s.Node, link.Upstream); err != nil {
				return err
			}
			return m.checkRelay(ctx, link.Upstream, RelayOptions{})
		}
	}
	return fmt.Errorf("未找到中转线路 %s", name)
}
