package main

import (
	"context"
	"fmt"
	"strings"
)

// A batch lease reserves a namespace slot without joining m.tunnels. Thus it
// cannot appear in subscriptions, be rebound to an inbound, persist as a client
// exit or trigger a panel update. One spare slot stays available for recovery.
func (m *Manager) reserveVerification(n Node) (*Tunnel, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tunnels {
		if t.Node.HostName == n.HostName {
			return nil, "active"
		}
		if t.Status != "up" {
			return nil, "recovery"
		}
	}
	if m.maxSlots-len(m.tunnels)-len(m.probes) < 2 {
		return nil, "capacity"
	}
	slot, err := m.freeSlot()
	if err != nil {
		return nil, "capacity"
	}
	cred, err := newSocksCred()
	if err != nil {
		return nil, "credentials"
	}
	t := &Tunnel{Slot: slot, Node: n, Cred: cred, Policy: SelectionPolicy{Mode: "auto", Country: n.CountryCode, ProbeOnly: true}}
	m.probes[slot] = t
	return t, ""
}

func (m *Manager) releaseVerification(t *Tunnel) {
	t.stop()
	m.mu.Lock()
	if m.probes[t.Slot] == t {
		delete(m.probes, t.Slot)
	}
	m.mu.Unlock()
}

func (m *Manager) probeVerification(ctx context.Context, n Node) VerificationResult {
	if ctx.Err() != nil {
		return VerificationResult{Status: "waiting", Detail: "任务暂停，等待恢复"}
	}
	q := m.quality.snapshot(n.HostName)
	if m.selection.blocked(n.IP) || m.selection.blocked(q.Identity.IP) || (residentialOnly() && !n.Residential) {
		return VerificationResult{Status: "skipped", Detail: "已排除或不符合家宽筛选，跳过"}
	}
	t, reason := m.reserveVerification(n)
	if t == nil {
		if reason == "active" {
			return VerificationResult{Status: "skipped", Detail: "正在使用，由原有健康检查负责；保留当前连接", IP: q.Identity.IP}
		}
		return VerificationResult{Status: "waiting", Detail: "等待空闲位置或现有出口恢复；为故障重连保留资源"}
	}
	defer m.releaseVerification(t)
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			t.cancelled.Store(true)
		case <-watchDone:
		}
	}()
	ip, latency, err := m.checkVerificationTunnel(ctx, t)
	if ctx.Err() == context.DeadlineExceeded {
		m.quality.record(n.HostName, false, 0)
		return VerificationResult{Status: "failed", Detail: "单个节点核验超时，继续下一个候选", IP: ip}
	}
	if ctx.Err() != nil {
		return VerificationResult{Status: "waiting", Detail: "核验已取消或超时，等待重试"}
	}
	m.quality.record(n.HostName, err == nil, latency)
	if err != nil {
		return VerificationResult{Status: "failed", Detail: err.Error(), IP: ip}
	}
	return VerificationResult{Status: "pass", Detail: "连接、实际出口、国家与 HTTPS 已核验；风险分需单个试连或选用时查询", IP: ip, LatencyMS: latency}
}

func (m *Manager) checkVerificationTunnel(ctx context.Context, t *Tunnel) (string, int64, error) {
	if err := t.setupNetns(); err != nil {
		return "", 0, fmt.Errorf("临时核验网络创建失败")
	}
	if err := t.installEgressGuard(); err != nil {
		return "", 0, fmt.Errorf("临时出口保护创建失败")
	}
	if err := t.startOpenVPN(m.workDir); err != nil {
		return "", 0, fmt.Errorf("VPN 连接失败或握手超时")
	}
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	probe, closeProbe, err := t.privateProbe()
	if err != nil {
		return "", 0, fmt.Errorf("无法创建本地核验连接")
	}
	defer closeProbe()
	result, ip := probeSOCKSExit(probe)
	if !result.OK {
		return "", 0, fmt.Errorf("实际出口查询失败或出口退回母机")
	}
	identity, err := lookupExitIdentity(probe, ip)
	if err != nil {
		return ip, 0, fmt.Errorf("实际出口地区查询失败")
	}
	m.quality.identify(t.Node.HostName, identity)
	if !strings.EqualFold(identity.Country, t.Node.CountryCode) || m.selection.blocked(ip) {
		return ip, 0, fmt.Errorf("实际出口国家不符或 IP 已排除")
	}
	if residentialOnly() && identity.Residential == "hosting" {
		return ip, 0, fmt.Errorf("实际出口为已知机房节点")
	}
	if ctx.Err() != nil {
		return ip, 0, ctx.Err()
	}
	web := probeWeb(probe)
	if !web.OK {
		return ip, web.LatencyMS, fmt.Errorf("出口 HTTPS 访问未通过")
	}
	// No public SOCKS listener, egressReady, IPPure query or panel operation.
	return ip, web.LatencyMS, nil
}
