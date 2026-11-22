package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"1mate/internal/model"
	"1mate/internal/sshx"
)

// 镜像同步：在能够拉取镜像的源主机上执行 docker save，通过 SSH 流式管道
// 直接传输到目标主机的 docker load，不在 1mate 服务器上落盘。

// ImageSyncPayload 是主机间镜像同步任务的负载。
type ImageSyncPayload struct {
	SourceHostID string   `json:"source_host_id"`
	TargetHostID string   `json:"target_host_id"`
	Images       []string `json:"images"`
}

func ParseImageSyncPayload(raw string) (ImageSyncPayload, error) {
	var p ImageSyncPayload
	if raw == "" || raw == "{}" {
		return p, fmt.Errorf("job payload is empty")
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	return p, nil
}

// TeslaMateImages 返回一个实例需要的镜像引用（按实例的 tag 拼接）。
func TeslaMateImages(inst *model.Instance) []string {
	images := []string{
		"teslamate/teslamate:" + inst.ImageTag,
		"postgres:" + inst.PostgresTag,
		"teslamate/grafana:" + inst.ImageTag,
	}
	if !inst.DisableMQTT {
		images = append(images, "eclipse-mosquitto:2")
	}
	return images
}

// requiredImagesForHost 汇总主机上所有实例需要的镜像（去重）。
func (s *Service) requiredImagesForHost(hostID string) ([]string, error) {
	insts, err := s.Store.ListInstances(hostID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, inst := range insts {
		// 镜像引用不需要解密 secrets，store 层实例即可。
		for _, img := range TeslaMateImages(&inst) {
			if !seen[img] {
				seen[img] = true
				out = append(out, img)
			}
		}
	}
	return out, nil
}

// ImagePrecheckResult 描述单个镜像在目标主机上的存在状态。
type ImagePrecheckResult struct {
	Image   string `json:"image"`
	Present bool   `json:"present"`
}

// ImagePrecheck 检查主机上缺失哪些镜像。只做只读操作，不修改远端。
func (s *Service) ImagePrecheck(hostID string, images []string) ([]ImagePrecheckResult, error) {
	h, err := s.Store.GetHost(hostID)
	if err != nil {
		return nil, err
	}
	caps, err := s.Store.GetCapabilities(hostID)
	if err != nil {
		return nil, fmt.Errorf("%w: probe the host first", ErrValidation)
	}
	if caps.UnsupportedReason != "" {
		return nil, fmt.Errorf("%w: %s", ErrValidation, caps.UnsupportedReason)
	}
	if len(images) == 0 {
		images, err = s.requiredImagesForHost(hostID)
		if err != nil {
			return nil, err
		}
	}
	// 没有实例时没有「所需镜像」可推导 —— 允许调用方显式传入 images 逐个
	// 检查；两者都为空则返回空结果而非错误，前端显示空状态即可。
	if len(images) == 0 {
		return []ImagePrecheckResult{}, nil
	}

	client, sudo, err := s.Connect(h)
	if err != nil {
		return nil, classifySSHError(err)
	}
	defer s.closeQuietly(client)

	// 一次 docker images 调用拿到全部已存在镜像，避免每个镜像跑一次 SSH。
	prefix := dockerPrefix(sudo, caps)
	res, err := client.Run(context.Background(), prefix+"docker images --format '{{.Repository}}:{{.Tag}}'",
		sshx.RunOptions{Timeout: 1 * time.Minute, Sudo: sudo})
	if err != nil {
		return nil, classifySSHError(err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("docker images failed (exit %d): %s", res.ExitCode, truncate(strings.TrimSpace(res.Stdout), 1000))
	}
	present := map[string]bool{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			present[line] = true
		}
	}

	out := make([]ImagePrecheckResult, 0, len(images))
	for _, img := range images {
		out = append(out, ImagePrecheckResult{Image: img, Present: present[img]})
	}
	return out, nil
}

// RunImageSyncJob 在源主机 docker save，流式写入目标主机 docker load。
// 数据经 1mate 进程内存转发但不落盘；docker load 对不完整流会整体失败，
// 目标主机不会留下半截镜像层。
func (s *Service) RunImageSyncJob(ctx context.Context, payload string, logf func(string, ...any)) error {
	p, err := ParseImageSyncPayload(payload)
	if err != nil {
		return err
	}
	if p.SourceHostID == p.TargetHostID {
		return fmt.Errorf("%w: source and target host must differ", ErrValidation)
	}
	if len(p.Images) == 0 {
		return fmt.Errorf("%w: no images specified", ErrValidation)
	}
	for _, img := range p.Images {
		if err := validateImageRef(img); err != nil {
			return err
		}
	}

	src, err := s.Store.GetHost(p.SourceHostID)
	if err != nil {
		return err
	}
	dst, err := s.Store.GetHost(p.TargetHostID)
	if err != nil {
		return err
	}
	srcCaps, err := s.Store.GetCapabilities(p.SourceHostID)
	if err != nil {
		return fmt.Errorf("%w: probe the source host first", ErrValidation)
	}
	dstCaps, err := s.Store.GetCapabilities(p.TargetHostID)
	if err != nil {
		return fmt.Errorf("%w: probe the target host first", ErrValidation)
	}

	if logf != nil {
		logf("镜像同步: %s (%s) → %s (%s)", src.Name, src.Address, dst.Name, dst.Address)
		logf("待同步镜像 %d 个:", len(p.Images))
		for _, img := range p.Images {
			logf("  - %s", img)
		}
	}

	srcClient, srcSudo, err := s.Connect(src)
	if err != nil {
		return classifySSHError(err)
	}
	defer s.closeQuietly(srcClient)
	dstClient, dstSudo, err := s.Connect(dst)
	if err != nil {
		return classifySSHError(err)
	}
	defer s.closeQuietly(dstClient)

	srcPrefix := dockerPrefix(srcSudo, srcCaps)
	dstPrefix := dockerPrefix(dstSudo, dstCaps)
	imgList := strings.Join(p.Images, " ")

	// 先校验源主机上镜像是否齐全，避免开传之后才发现缺镜像。
	checkRes, err := srcClient.Run(ctx, srcPrefix+"docker image inspect --format ok "+imgList,
		sshx.RunOptions{Timeout: 1 * time.Minute, Sudo: srcSudo})
	if err != nil {
		return classifySSHError(err)
	}
	if checkRes.ExitCode != 0 {
		return fmt.Errorf("源主机缺少指定镜像，请先在源主机上拉取: %s",
			truncate(strings.TrimSpace(checkRes.Stdout), 1500))
	}
	if logf != nil {
		logf("源主机镜像检查通过")
	}

	if logf != nil {
		logf("开始传输（源 docker save → 目标 docker load，不在平台落盘）")
	}

	// 源主机 stdout → io.Pipe → 目标主机 stdin。
	pr, pw := io.Pipe()
	saveDone := make(chan error, 1)
	go func() {
		_, err := srcClient.DownloadWithCommand(ctx,
			srcPrefix+"docker save "+imgList,
			sshx.RunOptions{Timeout: 2 * time.Hour, Sudo: srcSudo}, pw)
		// 关闭写端：正常结束给 EOF，出错把错误传给读端。
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		saveDone <- err
	}()

	loadErr := dstClient.RunWithStdin(ctx, dstPrefix+"docker load", pr,
		sshx.RunOptions{Timeout: 2 * time.Hour, Sudo: dstSudo})
	saveErr := <-saveDone
	if loadErr != nil {
		return fmt.Errorf("目标主机 docker load 失败: %w", classifySSHError(loadErr))
	}
	if saveErr != nil {
		return fmt.Errorf("源主机 docker save 失败: %w", classifySSHError(saveErr))
	}

	if logf != nil {
		logf("传输完成，校验目标主机镜像……")
	}
	verifyRes, err := dstClient.Run(ctx, dstPrefix+"docker image inspect --format ok "+imgList,
		sshx.RunOptions{Timeout: 1 * time.Minute, Sudo: dstSudo})
	if err != nil {
		return classifySSHError(err)
	}
	if verifyRes.ExitCode != 0 {
		return fmt.Errorf("同步后校验失败：目标主机缺少镜像 (exit %d)", verifyRes.ExitCode)
	}
	if logf != nil {
		logf("校验通过，%d 个镜像已在目标主机就绪", len(p.Images))
	}
	return nil
}

// validateImageRef 防止镜像引用注入 shell（只允许镜像名的合法字符）。
func validateImageRef(img string) error {
	if img == "" || len(img) > 200 {
		return fmt.Errorf("%w: invalid image reference", ErrValidation)
	}
	for _, c := range img {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '/' || c == ':' || c == '.' || c == '-' || c == '_' || c == '@'
		if !ok {
			return fmt.Errorf("%w: invalid character %q in image reference", ErrValidation, c)
		}
	}
	return nil
}
